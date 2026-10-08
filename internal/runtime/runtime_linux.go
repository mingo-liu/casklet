//go:build linux

package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/mingo-liu/mini-docker/internal/cgroup"
	"github.com/mingo-liu/mini-docker/internal/config"
	"github.com/mingo-liu/mini-docker/internal/ipc"
	"github.com/mingo-liu/mini-docker/internal/network"
	"github.com/mingo-liu/mini-docker/internal/rootfs"
	"golang.org/x/sys/unix"
)

func Run(cfg config.Config, stdin, stdout, stderr *os.File) (code int, runErr error) {
	return RunWithObserver(cfg, stdin, stdout, stderr, nil)
}

// RunWithObserver adds durable startup notifications for background supervisors.
// Cleanup completes before this function returns to its caller.
func RunWithObserver(cfg config.Config, stdin, stdout, stderr *os.File, observer Observer) (code int, runErr error) {
	return RunWithExec(cfg, stdin, stdout, stderr, observer, nil)
}

// RunWithExec publishes pinned resources for managed container execution.
func RunWithExec(cfg config.Config, stdin, stdout, stderr *os.File, observer Observer, executor Executor) (int, error) {
	return runWithExec(cfg, stdin, stdout, stderr, observer, executor, "", nil)
}

// RunManaged retains the private rootfs while isolating transient run resources.
func RunManaged(cfg config.Config, stdin, stdout, stderr *os.File, observer Observer, executor Executor, retainedRoot string, stoppingTimeout func() time.Duration) (int, error) {
	return runWithExec(cfg, stdin, stdout, stderr, observer, executor, retainedRoot, stoppingTimeout)
}

func runWithExec(cfg config.Config, stdin, stdout, stderr *os.File, observer Observer, executor Executor, retainedRoot string, stoppingTimeout func() time.Duration) (code int, runErr error) {
	code = 125
	if err := cfg.ValidateExecution(); err != nil {
		return code, err
	}
	if cfg.TTY && cfg.Interactive {
		if _, err := unix.IoctlGetTermios(terminalFD(stdin), unix.TCGETS); err != nil {
			return code, fmt.Errorf("interactive TTY requires terminal stdin: %w", err)
		}
	}
	incomingSignals := make(chan os.Signal, 8)
	signals := make(chan os.Signal, 8)
	// Remote clients and their watchdogs forward these signals for every run.
	// Catch them before preparation so cancellation still performs cleanup.
	signal.Notify(incomingSignals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	defer signal.Stop(incomingSignals)
	prepareCtx, cancelPrepare := context.WithTimeout(context.Background(), startupLimit)
	defer cancelPrepare()
	signalDone := make(chan struct{})
	defer close(signalDone)
	go func() {
		for {
			select {
			case sig := <-incomingSignals:
				select {
				case signals <- sig:
					// Queue the signal before cancellation so failed preparation can
					// report the requested signal exit code immediately.
					cancelPrepare()
				case <-signalDone:
					return
				}
			case <-signalDone:
				return
			}
		}
	}()
	if cfg.UserNS && (executor != nil || retainedRoot != "") {
		return code, errors.New("user namespaces currently support foreground runs only")
	}
	source, retainedReady, err := acquireRunTemplate(prepareCtx, cfg, retainedRoot)
	if err != nil {
		return preparationError(err, signals)
	}
	defer source.Close()
	cfg.RootFS = source.Path
	if err := checkConfig(cfg); err != nil {
		return code, err
	}
	if err := recoverExecutionRuns(prepareCtx, stderr, cfg.Rootless, cfg.UserNS); err != nil {
		return preparationError(err, signals)
	}
	run, err := createExecutionRun(prepareCtx, cfg.Rootless, cfg.UserNS)
	if err != nil {
		return preparationError(err, signals)
	}
	var networkLease *network.Lease
	defer func() {
		if networkLease != nil {
			defer networkLease.Close()
		}
		if err := run.remove(); err != nil {
			fmt.Fprintf(stderr, "mini-docker: cleanup %s: %v\n", run.path, err)
			runErr = errors.Join(runErr, cleanupFailure("run.remove", err))
		}
	}()
	if observer != nil {
		if err := observer(Event{Phase: "preparing", RunPath: run.path}); err != nil {
			return code, fmt.Errorf("record container preparation: %w", err)
		}
	}
	cfg.RootFS, err = prepareRunRootFS(prepareCtx, source.Path, run.path, retainedRoot, retainedReady, cfg.Image != "" && !cfg.UserNS)
	if err != nil {
		return preparationError(err, signals)
	}
	if cfg.OCI {
		if err := rootfs.PrepareImageWorkdir(cfg.RootFS, cfg); err != nil {
			return code, err
		}
	}
	if err := source.ReleaseCopyLease(); err != nil {
		return code, fmt.Errorf("release template copy lease: %w", err)
	}
	if cfg.UserNS && !cfg.Rootless {
		uid, _ := config.MappedID(0, cfg.UIDMappings)
		gid, _ := config.MappedID(0, cfg.GIDMappings)
		if err := os.Chown(run.path, int(uid), int(gid)); err != nil {
			return code, err
		}
		run.owner = uid
	}
	if err := mapRootOwnership(prepareCtx.Err, cfg.RootFS, cfg); err != nil {
		return code, fmt.Errorf("map rootfs ownership: %w", err)
	}
	if cfg.NetworkMode() == "bridge" {
		servers, err := network.Resolvers(cfg.DNS)
		if err != nil {
			return code, err
		}
		if err := rootfs.ConfigureDNS(cfg.RootFS, servers); err != nil {
			return code, fmt.Errorf("configure DNS: %w", err)
		}
	}
	select {
	case sig := <-signals:
		return 128 + int(sig.(syscall.Signal)), nil
	default:
	}
	group, err := cgroup.Create(cfg.Memory, cfg.PidsLimit, cfg.CPUQuota)
	if err != nil {
		return code, err
	}
	defer func() {
		runErr = errors.Join(runErr, cleanupWorkload(group, run, stderr))
	}()
	if err := run.record(group.Path()); err != nil {
		return code, err
	}
	var namespaces []*os.File
	var executable *os.File
	if executor != nil {
		if cfg.TTY {
			return code, errors.New("managed exec is unavailable for terminal runs")
		}
		defer func() {
			runErr = errors.Join(runErr, cleanupFailure("exec.close", executor.Close()))
			ipc.CloseFiles(namespaces)
			if executable != nil {
				executable.Close()
			}
		}()
	}
	event := Event{Phase: "prepared", RunPath: run.path, Cgroup: group.Path()}
	if observer != nil {
		if err := observer(event); err != nil {
			return code, fmt.Errorf("record container resources: %w", err)
		}
	}
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, 0)
	if err != nil {
		return code, err
	}
	control := os.NewFile(uintptr(fds[0]), "supervisor-control")
	childControl := os.NewFile(uintptr(fds[1]), "init-control")
	defer control.Close()
	defer childControl.Close()
	exe, err := os.Executable()
	if err != nil {
		return code, err
	}
	if executor != nil {
		executable, err = os.Open(exe)
		if err != nil {
			return code, err
		}
	}
	cmd := exec.Command(exe, "__init")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	if !cfg.Interactive || cfg.TTY {
		input, err := os.Open("/dev/null")
		if err != nil {
			return code, err
		}
		defer input.Close()
		cmd.Stdin = input
	}
	cmd.Env = baseEnvironment
	cmd.ExtraFiles = []*os.File{childControl, run.lock}
	var terminalSocket, childTerminal *os.File
	if cfg.TTY || executor != nil {
		fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, 0)
		if err != nil {
			return code, err
		}
		terminalSocket = os.NewFile(uintptr(fds[0]), "terminal-control")
		childTerminal = os.NewFile(uintptr(fds[1]), "init-terminal-control")
		defer terminalSocket.Close()
		defer childTerminal.Close()
		cmd.ExtraFiles = append(cmd.ExtraFiles, childTerminal)
	}

	if cfg.UserNS {
		binary, err := os.Open(exe)
		if err != nil {
			return code, err
		}
		defer binary.Close()
		cmd.ExtraFiles = append(cmd.ExtraFiles, binary)
		cmd.Path = fmt.Sprintf("/proc/self/fd/%d", 2+len(cmd.ExtraFiles))
	}
	cmd.SysProcAttr = namespaceAttributes(cfg)
	if err := cmd.Start(); err != nil {
		return code, fmt.Errorf("start container init: %w", err)
	}
	// Pin the namespace before Wait can reap init and permit host PID reuse.
	var networkNamespace *os.File
	if cfg.NetworkMode() == "bridge" {
		networkNamespace, err = os.Open(fmt.Sprintf("/proc/%d/ns/net", cmd.Process.Pid))
		if err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return code, fmt.Errorf("pin container network namespace: %w", err)
		}
		defer networkNamespace.Close()
	}
	childControl.Close()
	if childTerminal != nil {
		childTerminal.Close()
	}
	waited := make(chan error, 1)
	waitConsumed := false
	go func() { waited <- cmd.Wait() }()
	defer func() {
		// After the main loop, guarantee Wait has reaped init before disk cleanup.
		if !waitConsumed {
			if err := group.Kill(); err != nil {
				fmt.Fprintf(stderr, "mini-docker: stop init after cgroup kill failure: %v\n", err)
				_ = cmd.Process.Kill()
			}
			select {
			case <-waited:
				waitConsumed = true
			case <-time.After(stopGrace):
				run.keep = true
				runErr = errors.Join(runErr, cleanupFailure("init.wait", errors.New("init did not exit before the cleanup deadline")))
			}
		}
	}()
	if err := group.Add(cmd.Process.Pid); err != nil {
		// Init may still be outside the workload cgroup on a failed migration.
		cmd.Process.Kill()
		return code, fmt.Errorf("attach init to cgroup: %w", err)
	}
	if cfg.NetworkMode() == "bridge" {
		networkLease, err = network.Setup(prepareCtx, run.path, networkNamespace, cfg.Publish)
		if err != nil {
			return preparationError(fmt.Errorf("configure network: %w", err), signals)
		}
	}
	encoder := json.NewEncoder(control)
	if err := control.SetWriteDeadline(time.Now().Add(startupLimit)); err != nil {
		return code, err
	}
	if err := encoder.Encode(message{Kind: "prepare", Config: &cfg, ExecEnabled: executor != nil}); err != nil {
		return code, fmt.Errorf("configure init: %w", err)
	}
	events := make(chan message, 4)
	go func() {
		defer close(events)
		decoder := json.NewDecoder(control)
		for {
			var event message
			if err := decoder.Decode(&event); err != nil {
				return
			}
			events <- event
		}
	}()
	startupTimer := time.NewTimer(startupLimit)
	defer startupTimer.Stop()
	var timeoutTimer, killTimer *time.Timer
	var timeout, kill <-chan time.Time
	defer func() {
		if timeoutTimer != nil {
			timeoutTimer.Stop()
		}
		if killTimer != nil {
			killTimer.Stop()
		}
	}()
	authorized, started, mainExited, stopping, timedOut := false, false, false, false, false
	var terminalErrors <-chan error
	recordStarted := func() error {
		if started {
			return nil
		}
		if executor != nil {
			if err := executor.Start(ExecResources{Namespaces: namespaces[:5], Root: namespaces[5], Executable: executable, Group: group, Config: cfg}); err != nil {
				return fmt.Errorf("enable container exec: %w", err)
			}
		}
		if observer != nil {
			event.Phase = "started"
			if err := observer(event); err != nil {
				return fmt.Errorf("record command startup: %w", err)
			}
		}
		started = true
		return nil
	}
	commandExit := 125
	stopTimeout := func() {
		timeout = nil
		if timeoutTimer != nil {
			timeoutTimer.Stop()
		}
	}
	for {
		select {
		case event, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			switch event.Kind {
			case "ready":
				if stopping {
					continue
				}
				if executor != nil {
					var err error
					namespaces, err = receiveExecNamespaces(terminalFD(terminalSocket))
					if err != nil {
						return 125, fmt.Errorf("receive container namespaces: %w", err)
					}
				}
				if cfg.TTY {
					master, err := receiveTerminal(terminalFD(terminalSocket))
					if err != nil {
						return 125, fmt.Errorf("receive container terminal: %w", err)
					}
					bridge, err := startTerminalBridge(master, stdin, stdout, cfg.Interactive)
					if err != nil {
						master.Close()
						return 125, err
					}
					terminalErrors = bridge.errors
					defer func() {
						if err := bridge.close(stdout); err != nil {
							runErr = errors.Join(runErr, cleanupFailure("terminal.close", err))
						}
					}()
				}
				if err := encoder.Encode(message{Kind: "start"}); err != nil {
					return 125, fmt.Errorf("authorize command: %w", err)
				}
				authorized = true
			case "started":
				if err := recordStarted(); err != nil {
					return 125, err
				}
				startupTimer.Stop()
				if cfg.Timeout > 0 && !stopping {
					timeoutTimer = time.NewTimer(cfg.Timeout)
					timeout = timeoutTimer.C
				}
			case "exited":
				mainExited, commandExit = true, event.ExitCode
				stopTimeout()
				if !stopping {
					stopping = true
					// Give init time to kill and reap remaining descendants before
					// enforcing the supervisor's final shutdown boundary.
					killTimer = time.NewTimer(cfg.StoppingTimeout() + time.Second)
					kill = killTimer.C
				}
			case "error":
				return 125, fmt.Errorf("container startup: %s", event.Error)
			}
		case err := <-waited:
			waitConsumed = true
			// Process all final messages before using init's exit status. A quick
			// command or forced cleanup can race with the event reader.
			for events != nil {
				event, ok := <-events
				if !ok {
					break
				}
				switch event.Kind {
				case "error":
					if !timedOut {
						return 125, errors.New(event.Error)
					}
				case "started":
					if err := recordStarted(); err != nil {
						return 125, err
					}
				case "exited":
					mainExited, commandExit = true, event.ExitCode
				}
			}
			if timedOut {
				return 124, nil
			}
			if mainExited {
				return commandExit, nil
			}
			if !started && !stopping {
				return 125, fmt.Errorf("init exited before starting the command: %v", err)
			}
			return processExitCode(cmd.ProcessState), nil
		case sig := <-signals:
			if !authorized {
				// No command can have started before authorization. Abort setup
				// promptly rather than leaving init blocked on its start message.
				return 128 + int(sig.(syscall.Signal)), nil
			}
			grace := cfg.StoppingTimeout()
			if sig == syscall.SIGTERM && stoppingTimeout != nil {
				grace = stoppingTimeout()
			}
			if !stopping {
				stopping = true
				stopTimeout()
				killTimer = time.NewTimer(grace)
				kill = killTimer.C
			}
			if sig == syscall.SIGTERM && stoppingTimeout != nil {
				_ = control.SetWriteDeadline(time.Now().Add(time.Second))
				if err := encoder.Encode(message{Kind: "stop", StopTimeout: grace}); err != nil {
					_ = cmd.Process.Signal(sig)
				}
			} else {
				_ = cmd.Process.Signal(sig)
			}
		case err := <-terminalErrors:
			return 125, fmt.Errorf("terminal I/O: %w", err)
		case <-timeout:
			timedOut, stopping, timeout = true, true, nil
			_ = cmd.Process.Signal(syscall.SIGTERM)
			killTimer = time.NewTimer(cfg.StoppingTimeout())
			kill = killTimer.C
		case <-kill:
			kill = nil
			if err := group.Kill(); err != nil {
				fmt.Fprintf(stderr, "mini-docker: stop init after cgroup kill failure: %v\n", err)
				if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
					return 125, fmt.Errorf("force container shutdown: %w", err)
				}
			}
		case <-startupTimer.C:
			return 125, errors.New("container startup timed out")
		}
	}
}

func preparationError(err error, signals <-chan os.Signal) (int, error) {
	select {
	case sig := <-signals:
		return 128 + int(sig.(syscall.Signal)), nil
	default:
		return 125, err
	}
}

func processExitCode(state *os.ProcessState) int {
	if state == nil {
		return 125
	}
	if status, ok := state.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return 128 + int(status.Signal())
	}
	return state.ExitCode()
}
