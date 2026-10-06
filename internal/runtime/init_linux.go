//go:build linux

package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/mingo-liu/mini-docker/internal/config"
	"github.com/mingo-liu/mini-docker/internal/rootfs"
	"golang.org/x/sys/unix"
)

func Init() int {
	if os.Getpid() != 1 {
		fmt.Fprintln(os.Stderr, "mini-docker: internal init requires container PID 1")
		return 125
	}
	if err := unix.SetNonblock(3, true); err != nil {
		return 125
	}
	unix.CloseOnExec(3)
	unix.CloseOnExec(4)
	control := os.NewFile(3, "init-control")
	lock := os.NewFile(4, "run-lock")
	defer control.Close()
	defer lock.Close()
	encoder := json.NewEncoder(control)
	fail := func(err error) int {
		_ = encoder.Encode(message{Kind: "error", Error: err.Error()})
		return 125
	}
	if err := control.SetDeadline(time.Now().Add(startupLimit)); err != nil {
		return fail(err)
	}
	decoder := json.NewDecoder(control)
	decoder.DisallowUnknownFields()
	var prepare message
	if err := decoder.Decode(&prepare); err != nil {
		return fail(err)
	}
	if prepare.Kind != "prepare" || prepare.Config == nil || len(prepare.Config.Command) == 0 {
		return fail(errors.New("invalid init configuration"))
	}
	cfg := prepare.Config
	if err := cfg.ValidateExecution(); err != nil {
		return fail(err)
	}
	signals := make(chan os.Signal, 8)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGCHLD)
	if cfg.TTY {
		signal.Notify(signals, syscall.SIGHUP, syscall.SIGQUIT)
	}
	defer signal.Stop(signals)
	if err := rootfs.Setup(cfg.RootFS, cfg.ReadOnly); err != nil {
		return fail(err)
	}
	if err := unix.Sethostname([]byte(cfg.Hostname)); err != nil {
		return fail(err)
	}
	if err := enableLoopback(); err != nil {
		return fail(err)
	}
	if prepare.ExecEnabled {
		if cfg.TTY {
			return fail(errors.New("terminal init does not accept managed exec"))
		}
		unix.CloseOnExec(5)
		err := sendExecNamespaces(5)
		unix.Close(5)
		if err != nil {
			return fail(fmt.Errorf("share container namespaces: %w", err))
		}
	}
	var terminal, terminalMaster *os.File
	if cfg.TTY {
		unix.CloseOnExec(5)
		master, slave, err := prepareTerminal(cfg.User)
		if err != nil {
			return fail(fmt.Errorf("prepare terminal: %w", err))
		}
		terminal = slave
		terminalMaster = master
		defer terminal.Close()
		defer terminalMaster.Close()
		err = sendTerminal(5, master)
		unix.Close(5)
		if err != nil {
			return fail(fmt.Errorf("send terminal: %w", err))
		}
	}
	if err := reducePrivileges(cfg.User); err != nil {
		return fail(err)
	}
	if err := encoder.Encode(message{Kind: "ready"}); err != nil {
		return 125
	}
	var start message
	if err := decoder.Decode(&start); err != nil {
		return fail(err)
	}
	if start.Kind != "start" {
		return fail(errors.New("invalid command authorization"))
	}
	// No children exist yet, so only external stop signals can be pending.
	select {
	case sig := <-signals:
		return 128 + int(sig.(syscall.Signal))
	default:
	}
	if err := control.SetDeadline(time.Time{}); err != nil {
		return fail(err)
	}
	if err := os.Chdir(cfg.WorkingDirectory()); err != nil {
		return fail(fmt.Errorf("enter working directory %s: %w", cfg.WorkingDirectory(), err))
	}
	commandEnvironment := cfg.CommandEnvironment()
	// LookPath uses the init process's PATH. Resolve inside the container using
	// the same explicitly configured PATH that the command receives.
	for _, assignment := range commandEnvironment {
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
	attributes := &os.ProcAttr{
		Dir: cfg.WorkingDirectory(), Env: commandEnvironment,
		Files: []*os.File{os.Stdin, os.Stdout, os.Stderr},
		Sys:   &syscall.SysProcAttr{Setpgid: true},
	}
	if terminal != nil {
		attributes.Files = []*os.File{terminal, terminal, terminal}
		attributes.Sys = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	}
	process, err := os.StartProcess(executable, cfg.Command, attributes)
	if err != nil {
		return fail(err)
	}
	defer process.Release()
	if err := encoder.Encode(message{Kind: "started"}); err != nil {
		_ = unix.Kill(-1, unix.SIGKILL)
		return 125
	}
	orphaned := make(chan struct{})
	go func() {
		var unexpected message
		_ = decoder.Decode(&unexpected)
		close(orphaned)
	}()
	mainExited, stopping := false, false
	exitCode := 125
	var shutdownTimer *time.Timer
	var shutdown <-chan time.Time
	defer func() {
		if shutdownTimer != nil {
			shutdownTimer.Stop()
		}
	}()
	beginShutdown := func() {
		if !stopping {
			stopping = true
			shutdownTimer = time.NewTimer(stopGrace)
			shutdown = shutdownTimer.C
		}
	}
	for {
		// Reap every available child before sleeping. SIGCHLD wakes this loop
		// without polling while idle, including for multiple coalesced exits.
		for {
			var status unix.WaitStatus
			pid, err := unix.Wait4(-1, &status, unix.WNOHANG, nil)
			if errors.Is(err, unix.EINTR) {
				continue
			}
			if errors.Is(err, unix.ECHILD) {
				if mainExited {
					return exitCode
				}
				return 125
			}
			if err != nil {
				return fail(fmt.Errorf("reap child: %w", err))
			}
			if pid == 0 {
				break
			}
			if pid == process.Pid {
				mainExited = true
				exitCode = status.ExitStatus()
				if status.Signaled() {
					exitCode = 128 + int(status.Signal())
				}
				if err := encoder.Encode(message{Kind: "exited", ExitCode: exitCode}); err != nil {
					_ = unix.Kill(-1, unix.SIGKILL)
					return exitCode
				}
				_ = unix.Kill(-1, unix.SIGTERM)
				beginShutdown()
			}
		}
		select {
		case sig := <-signals:
			if sig == syscall.SIGCHLD {
				continue
			}
			group := process.Pid
			if terminalMaster != nil {
				// Init is outside the command's session. The master permits
				// foreground-group lookup without owning its controlling TTY.
				if foreground, err := unix.IoctlGetInt(terminalFD(terminalMaster), unix.TIOCGPGRP); err == nil && foreground > 0 {
					group = foreground
				}
			}
			_ = unix.Kill(-group, sig.(syscall.Signal))
			beginShutdown()
		case <-orphaned:
			orphaned = nil
			_ = unix.Kill(-1, unix.SIGTERM)
			beginShutdown()
		case <-shutdown:
			shutdown = nil
			_ = unix.Kill(-1, unix.SIGKILL)
		}
	}
}

func enableLoopback() error {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open loopback control socket: %w", err)
	}
	defer unix.Close(fd)
	request, err := unix.NewIfreq("lo")
	if err != nil {
		return err
	}
	if err := unix.IoctlIfreq(fd, unix.SIOCGIFFLAGS, request); err != nil {
		return err
	}
	request.SetUint16(request.Uint16() | unix.IFF_UP)
	if err := unix.IoctlIfreq(fd, unix.SIOCSIFFLAGS, request); err != nil {
		return fmt.Errorf("enable loopback: %w", err)
	}
	return nil
}

// Apply restrictions to every Go thread so fork/exec cannot select an
// unrestricted thread. The runtime is built with CGO_ENABLED=0.
func reducePrivileges(user *config.User) error {
	data, err := os.ReadFile("/proc/sys/kernel/cap_last_cap")
	if err != nil {
		return err
	}
	last, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || last < 0 || last > 63 {
		return errors.New("unsupported Linux capability range")
	}
	prctl := func(option, arg2 uintptr) error {
		_, _, errno := syscall.AllThreadsSyscall6(unix.SYS_PRCTL, option, arg2, 0, 0, 0, 0)
		if errno != 0 {
			return errno
		}
		return nil
	}
	if err := prctl(unix.PR_SET_NO_NEW_PRIVS, 1); err != nil {
		return fmt.Errorf("set no_new_privs on all threads: %w", err)
	}
	if err := prctl(unix.PR_CAP_AMBIENT, unix.PR_CAP_AMBIENT_CLEAR_ALL); err != nil {
		return err
	}
	for capability := 0; capability <= last; capability++ {
		if err := prctl(unix.PR_CAPBSET_DROP, uintptr(capability)); err != nil {
			return fmt.Errorf("drop capability %d: %w", capability, err)
		}
	}
	// Change the init identity as well as its children. This lets init discard
	// SETUID/SETGID completely before exec while still signaling its workload.
	// The syscall package applies these changes to every Go thread when built
	// without cgo. Clear inherited supplementary groups even for UID 0.
	if err := syscall.Setgroups(nil); err != nil {
		return fmt.Errorf("clear supplementary groups: %w", err)
	}
	if user != nil {
		if err := syscall.Setresgid(int(user.GID), int(user.GID), int(user.GID)); err != nil {
			return fmt.Errorf("set container group: %w", err)
		}
		if err := syscall.Setresuid(int(user.UID), int(user.UID), int(user.UID)); err != nil {
			return fmt.Errorf("set container user: %w", err)
		}
	}
	header := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	capabilities := [2]unix.CapUserData{}
	_, _, errno := syscall.AllThreadsSyscall(unix.SYS_CAPSET, uintptr(unsafe.Pointer(&header)), uintptr(unsafe.Pointer(&capabilities[0])), 0)
	if errno != 0 {
		return fmt.Errorf("clear capabilities on all threads: %w", errno)
	}
	return nil
}
