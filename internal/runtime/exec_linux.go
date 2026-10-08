//go:build linux

package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	goruntime "runtime"
	"strings"
	"syscall"
	"time"

	"github.com/mingo-liu/casklet/internal/config"
	"golang.org/x/sys/unix"
)

const execConfigLimit = 64 << 10

const execConfigSeals = unix.F_SEAL_SEAL | unix.F_SEAL_WRITE | unix.F_SEAL_GROW | unix.F_SEAL_SHRINK

// ExecuteInContainer isolates each added command in a child cgroup. The
// namespace descriptors come from the live init, rather than a reusable PID.
func ExecuteInContainer(ctx context.Context, resources ExecResources, request config.Exec, stdin, stdout, stderr *os.File, signals <-chan syscall.Signal, terminal ExecTerminal) (code int, runErr error) {
	return executeInContainer(ctx, resources, request, stdin, stdout, stderr, signals, terminal, false)
}

// ExecuteProbe uses the normal exec isolation but kills immediately on deadline
// or cancellation. Its streams are discarded; descendants share a child cgroup.
func ExecuteProbe(ctx context.Context, resources ExecResources, command []string, null *os.File) (int, error) {
	return executeInContainer(ctx, resources, config.Exec{Command: command}, null, null, null, nil, nil, true)
}

func executeInContainer(ctx context.Context, resources ExecResources, request config.Exec, stdin, stdout, stderr *os.File, signals <-chan syscall.Signal, terminal ExecTerminal, hardStop bool) (code int, runErr error) {
	code = 125
	if err := request.Validate(); err != nil {
		return code, err
	}
	if request.TTY && terminal == nil {
		return code, errors.New("TTY exec requires a terminal client")
	}
	if os.Geteuid() != 0 || len(resources.Namespaces) != 5 || resources.Root == nil || resources.Executable == nil || resources.Group == nil {
		return code, errors.New("container exec requires root and live container resources")
	}
	for _, file := range resources.Namespaces {
		if file == nil {
			return code, errors.New("container exec namespace descriptor is missing")
		}
	}
	if err := ctx.Err(); err != nil {
		return code, err
	}
	cfg := request.Apply(resources.Config)
	if err := cfg.ValidateExecution(); err != nil {
		return code, err
	}
	control, err := sealExecConfig(cfg)
	if err != nil {
		return code, err
	}
	defer control.Close()
	group, err := resources.Group.NewChild()
	if err != nil {
		return code, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), stopGrace)
		defer cancel()
		killErr := group.Kill()
		waitErr := group.WaitEmpty(cleanup)
		var closeErr error
		if waitErr == nil {
			closeErr = group.Close()
		}
		runErr = errors.Join(runErr, killErr, waitErr, closeErr)
	}()
	groupFile, err := os.Open(group.Path())
	if err != nil {
		return code, fmt.Errorf("open exec cgroup: %w", err)
	}
	defer groupFile.Close()
	var terminalParent, terminalChild *os.File
	if cfg.TTY {
		pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, 0)
		if err != nil {
			return code, fmt.Errorf("create exec terminal channel: %w", err)
		}
		terminalParent = os.NewFile(uintptr(pair[0]), "exec-terminal-parent")
		terminalChild = os.NewFile(uintptr(pair[1]), "exec-terminal-child")
		defer terminalParent.Close()
		defer terminalChild.Close()
	}
	// Resolve the executable in the child after ExtraFiles maps the pinned
	// binary to descriptor 4. Replacing or deleting the launcher on disk
	// cannot change the helper used by an already running container.
	cmd := exec.Command("/proc/self/fd/4", "__enter")
	cmd.Args[0] = "casklet"
	cmd.Env = baseEnvironment
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	cmd.ExtraFiles = append([]*os.File{control, resources.Executable}, resources.Namespaces...)
	cmd.ExtraFiles = append(cmd.ExtraFiles, resources.Root, groupFile)
	if terminalChild != nil {
		cmd.ExtraFiles = append(cmd.ExtraFiles, terminalChild)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	if err := cmd.Start(); err != nil {
		return code, fmt.Errorf("start exec namespace helper: %w", err)
	}
	if terminalChild != nil {
		terminalChild.Close()
	}
	finished := make(chan error, 1)
	go func() { finished <- cmd.Wait() }()
	var terminalResult <-chan error
	var terminalError error
	terminalContext, cancelTerminal := context.WithTimeout(ctx, execTerminalStartupLimit)
	defer cancelTerminal()
	if terminalParent != nil {
		result := make(chan error, 1)
		terminalResult = result
		go func() { result <- publishExecTerminal(terminalContext, terminalParent, terminal) }()
		defer func() {
			cancelTerminal()
			if terminalResult != nil {
				// The callback contract requires cancellation awareness. Keep
				// the socket alive until its worker has finished using it.
				<-terminalResult
			}
		}()
	}
	var timeout, grace *time.Timer
	var deadline, forced <-chan time.Time
	if request.Timeout > 0 {
		timeout = time.NewTimer(request.Timeout)
		deadline = timeout.C
		defer timeout.Stop()
	}
	defer func() {
		if grace != nil {
			grace.Stop()
		}
	}()
	interrupted, timedOut := false, false
	done := ctx.Done()
	beginStop := func(sig syscall.Signal) {
		cancelTerminal()
		if hardStop {
			_ = group.Kill()
			_ = cmd.Process.Kill()
		} else {
			_ = cmd.Process.Signal(sig)
		}
		if grace == nil {
			grace = time.NewTimer(stopGrace)
			forced = grace.C
		}
	}
	for {
		select {
		case err := <-finished:
			if terminalError != nil {
				return 125, terminalError
			}
			if timedOut {
				return 124, nil
			}
			if err == nil {
				return 0, nil
			}
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				return execStatus(exitErr.ProcessState), nil
			}
			return 125, fmt.Errorf("wait for exec helper: %w", err)
		case err := <-terminalResult:
			terminalResult = nil
			if err != nil && !interrupted && !timedOut {
				terminalError = fmt.Errorf("prepare exec terminal: %w", err)
				beginStop(syscall.SIGTERM)
			}
		case sig, ok := <-signals:
			if !ok {
				signals = nil
				continue
			}
			if execSignal(sig) {
				interrupted = true
				beginStop(sig)
			}
		case <-done:
			done = nil
			interrupted = true
			beginStop(syscall.SIGTERM)
		case <-deadline:
			deadline = nil
			timedOut = true
			beginStop(syscall.SIGTERM)
		case <-forced:
			forced = nil
			_ = group.Kill()
			_ = cmd.Process.Kill()
			// An uninterruptible kernel wait must not block the endpoint
			// forever. The waiter still owns and eventually reaps this child.
			var err error
			select {
			case err = <-finished:
			case <-time.After(stopGrace):
				return 125, errors.New("exec namespace helper did not exit after SIGKILL")
			}
			if terminalError != nil {
				return 125, terminalError
			}
			if timedOut {
				return 124, nil
			}
			if interrupted {
				return 137, nil
			}
			return 125, err
		}
	}
}

func sealExecConfig(cfg config.Config) (*os.File, error) {
	data, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	if len(data) > execConfigLimit {
		return nil, errors.New("exec configuration exceeds 64 KiB")
	}
	fd, err := unix.MemfdCreate("casklet-exec", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return nil, fmt.Errorf("create exec configuration: %w", err)
	}
	file := os.NewFile(uintptr(fd), "exec-config")
	if _, err = file.Write(data); err == nil {
		_, err = unix.FcntlInt(file.Fd(), unix.F_ADD_SEALS, execConfigSeals)
	}
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("seal exec configuration: %w", err)
	}
	return file, nil
}

func readExecConfig(fd int) (config.Config, error) {
	var cfg config.Config
	seals, err := unix.FcntlInt(uintptr(fd), unix.F_GET_SEALS, 0)
	if err != nil || seals&execConfigSeals != execConfigSeals {
		return cfg, errors.New("exec configuration must be an immutable sealed memfd")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Size <= 0 || stat.Size > execConfigLimit || stat.Uid != 0 {
		return cfg, errors.New("exec configuration must be root-owned and at most 64 KiB")
	}
	data := make([]byte, stat.Size)
	n, err := unix.Pread(fd, data, 0)
	if err != nil || n != len(data) {
		return cfg, fmt.Errorf("read exec configuration: %w", errors.Join(err, io.ErrUnexpectedEOF))
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("decode exec configuration: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return cfg, errors.New("exec configuration contains trailing data")
	}
	if cfg.Timeout < 0 {
		return cfg, errors.New("invalid exec configuration")
	}
	return cfg, cfg.ValidateExecution()
}

// EnterExec is a privileged namespace helper. Its user child is created in the
// target PID namespace and child cgroup before any user code can run.
func EnterExec() int {
	fail := func(err error) int {
		fmt.Fprintln(os.Stderr, "casklet: exec:", err)
		return 125
	}
	if os.Geteuid() != 0 {
		return fail(errors.New("namespace entry requires root"))
	}
	cfg, err := readExecConfig(3)
	if err != nil {
		return fail(err)
	}
	// ExtraFiles deliberately arrive without CLOEXEC. Only configuration and
	// executable are explicitly remapped for the bootstrap; in particular the
	// host cgroup descriptor must not survive into container user code.
	for fd := 3; fd <= 11; fd++ {
		unix.CloseOnExec(fd)
	}
	if cfg.TTY {
		unix.CloseOnExec(12)
		if err := unix.SetNonblock(12, true); err != nil {
			return fail(fmt.Errorf("prepare terminal channel: %w", err))
		}
	}
	signals := make(chan os.Signal, 16)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT, syscall.SIGCHLD)
	defer signal.Stop(signals)
	goruntime.LockOSThread()
	// Keep this thread locked until process exit: its namespaces and root are
	// intentionally different from the other Go runtime threads.
	if err := unix.Unshare(unix.CLONE_FS); err != nil {
		return fail(fmt.Errorf("unshare filesystem state: %w", err))
	}
	for _, namespace := range []struct{ fd, kind int }{
		{6, unix.CLONE_NEWUTS}, {7, unix.CLONE_NEWIPC}, {8, unix.CLONE_NEWNET}, {9, unix.CLONE_NEWPID}, {5, unix.CLONE_NEWNS},
	} {
		if err := unix.Setns(namespace.fd, namespace.kind); err != nil {
			return fail(fmt.Errorf("join namespace descriptor %d: %w", namespace.fd, err))
		}
	}
	if err := unix.Fchdir(10); err != nil {
		return fail(fmt.Errorf("enter container root: %w", err))
	}
	if err := unix.Chroot("."); err != nil {
		return fail(fmt.Errorf("select container root: %w", err))
	}
	if err := unix.Chdir("/"); err != nil {
		return fail(err)
	}
	for fd := 5; fd <= 10; fd++ {
		_ = unix.Close(fd)
	}
	var master, slave *os.File
	var terminalSocket, terminalGroup *os.File
	if cfg.TTY {
		terminalSocket = os.NewFile(12, "exec-terminal-channel")
		terminalGroup = os.NewFile(11, "exec-terminal-cgroup")
		defer terminalSocket.Close()
		defer terminalGroup.Close()
		master, slave, err = openTerminal(cfg.User)
		if err != nil {
			return fail(err)
		}
		defer master.Close()
		defer slave.Close()
		if err := authorizeExecTerminal(12, master, signals); err != nil {
			var stopped execTerminalStopped
			if errors.As(err, &stopped) {
				return 128 + int(stopped.signal)
			}
			return fail(err)
		}
	}
	select {
	case sig := <-signals:
		return 128 + int(sig.(syscall.Signal))
	default:
	}
	control := os.NewFile(3, "exec-config")
	binary := os.NewFile(4, "exec-binary")
	// Go's Pdeathsig handshake compares getppid with the parent's PID. The
	// parent is outside this child's PID namespace, so getppid is zero and
	// that check would kill a healthy child. The exec cgroup owns cleanup.
	attributes := &os.ProcAttr{
		Env:   baseEnvironment,
		Files: []*os.File{os.Stdin, os.Stdout, os.Stderr, control, binary},
		Sys:   &syscall.SysProcAttr{Setpgid: true, UseCgroupFD: true, CgroupFD: 11},
	}
	if slave != nil {
		attributes.Files = []*os.File{slave, slave, slave, control, binary, os.Stderr}
		attributes.Sys.Setpgid = false
		attributes.Sys.Setsid = true
		attributes.Sys.Setctty = true
		attributes.Sys.Ctty = 0
	}
	process, err := os.StartProcess("/proc/self/fd/4", []string{"casklet", "__exec"}, attributes)
	control.Close()
	binary.Close()
	if !cfg.TTY {
		_ = unix.Close(11)
	}
	if cfg.TTY {
		terminalSocket.Close()
		slave.Close()
	}
	if err != nil {
		return fail(fmt.Errorf("start container exec bootstrap: %w", err))
	}
	defer process.Release()
	for {
		// Reap on this thread before accepting another signal. A separate
		// waiter could reap and release the PID before a queued signal is
		// forwarded to the old process group. Keeping our child unreaped
		// until this check prevents that group ID from being reused.
		var status unix.WaitStatus
		pid, err := unix.Wait4(process.Pid, &status, unix.WNOHANG, nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return fail(fmt.Errorf("reap exec child: %w", err))
		}
		if pid == process.Pid {
			if status.Signaled() {
				return 128 + int(status.Signal())
			}
			return status.ExitStatus()
		}
		select {
		case sig := <-signals:
			if sig == syscall.SIGCHLD {
				continue
			}
			if master != nil {
				// TIOCGPGRP uses the calling task's active PID namespace. This
				// helper remains in the host PID namespace after setns, so the
				// returned group can be signaled directly from here.
				forwardExecTerminalSignal(master, 11, process.Pid, sig.(syscall.Signal))
				continue
			}
			_ = unix.Kill(-process.Pid, sig.(syscall.Signal))
		}
	}
}

// ExecInit executes only inside the target namespaces and resource group.
func ExecInit() int {
	diagnostics := os.Stderr
	fail := func(err error) int {
		fmt.Fprintln(diagnostics, "casklet: exec:", err)
		return 125
	}
	if os.Geteuid() != 0 || os.Getpid() == 1 {
		return fail(errors.New("exec bootstrap requires a privileged non-init child"))
	}
	cfg, err := readExecConfig(3)
	if err != nil {
		return fail(err)
	}
	if cfg.TTY {
		diagnostics = os.NewFile(5, "exec-diagnostics")
		defer diagnostics.Close()
		unix.CloseOnExec(5)
		// Hide the host diagnostic descriptor from other container processes
		// while bootstrap drops privileges. Successful exec closes the
		// descriptor and restores normal dumpability for the user command.
		if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
			return fail(fmt.Errorf("protect exec diagnostics: %w", err))
		}
	}
	_ = unix.Close(3)
	_ = unix.Close(4)
	if err := reducePrivileges(cfg.User, cfg.OCI); err != nil {
		return fail(err)
	}
	if err := installSeccomp(cfg.SeccompProfile()); err != nil {
		return fail(err)
	}
	if cfg.TTY {
		// Credential changes may reset dumpability according to host policy.
		if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
			return fail(fmt.Errorf("protect exec diagnostics after privilege reduction: %w", err))
		}
	}
	if err := os.Chdir(cfg.WorkingDirectory()); err != nil {
		return fail(fmt.Errorf("enter working directory: %w", err))
	}
	env := cfg.CommandEnvironment()
	for _, assignment := range env {
		if value, ok := strings.CutPrefix(assignment, "PATH="); ok {
			if err := os.Setenv("PATH", value); err != nil {
				return fail(err)
			}
			break
		}
	}
	executable, err := exec.LookPath(cfg.Command[0])
	if err != nil {
		return fail(err)
	}
	if err := unix.Exec(executable, cfg.Command, env); err != nil {
		return fail(err)
	}
	return 125
}

func execSignal(sig syscall.Signal) bool {
	return sig == syscall.SIGINT || sig == syscall.SIGTERM || sig == syscall.SIGHUP || sig == syscall.SIGQUIT
}

func execStatus(state *os.ProcessState) int {
	if status, ok := state.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return 128 + int(status.Signal())
	}
	return state.ExitCode()
}
