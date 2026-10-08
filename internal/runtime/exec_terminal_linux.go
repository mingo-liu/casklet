//go:build linux

package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mingo-liu/casklet/internal/ipc"
	"golang.org/x/sys/unix"
)

const execTerminalStartupLimit = 10 * time.Second

type execTerminalStopped struct{ signal syscall.Signal }

func (e execTerminalStopped) Error() string {
	return "terminal setup interrupted by " + e.signal.String()
}

func publishExecTerminal(ctx context.Context, socket *os.File, terminal ExecTerminal) error {
	fd := terminalFD(socket)
	var master *os.File
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		data, files, err := ipc.ReceiveFD(fd, 1)
		if err == nil {
			if len(data) != 1 || data[0] != 'T' || len(files) != 1 {
				ipc.CloseFiles(files)
				return errors.New("invalid exec terminal descriptor message")
			}
			master = files[0]
			if _, err := unix.IoctlGetInt(terminalFD(master), unix.TIOCGPTN); err != nil {
				master.Close()
				return errors.New("exec terminal descriptor is not a PTY master")
			}
			if err := unix.SetNonblock(terminalFD(master), true); err != nil {
				master.Close()
				return err
			}
			break
		}
		if !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EINTR) {
			return err
		}
		if err := pollExecTerminal(ctx, fd, unix.POLLIN); err != nil {
			return err
		}
	}
	defer master.Close()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := terminal(ctx, master); err != nil {
		return err
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := unix.Sendmsg(fd, []byte{'R'}, nil, nil, unix.MSG_DONTWAIT)
		if err == nil {
			return nil
		}
		if !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EINTR) {
			return err
		}
		if err := pollExecTerminal(ctx, fd, unix.POLLOUT); err != nil {
			return err
		}
	}
}

func pollExecTerminal(ctx context.Context, fd int, events int16) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		fds := []unix.PollFd{{Fd: int32(fd), Events: events}}
		n, err := unix.Poll(fds, 100)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
		if n > 0 {
			return nil
		}
	}
}

// authorizeExecTerminal is synchronous on the helper's locked namespace thread.
// No user process starts until the terminal client has configured its PTY.
func authorizeExecTerminal(fd int, master *os.File, signals <-chan os.Signal) error {
	ctx, cancel := context.WithTimeout(context.Background(), execTerminalStartupLimit)
	defer cancel()
	check := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		select {
		case signal := <-signals:
			if sig, ok := signal.(syscall.Signal); ok && execSignal(sig) {
				return execTerminalStopped{sig}
			}
		default:
		}
		return nil
	}
	for {
		if err := check(); err != nil {
			return err
		}
		err := sendTerminal(fd, master)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EINTR) {
			return fmt.Errorf("publish exec terminal: %w", err)
		}
		if _, err := unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLOUT}}, 100); err != nil && !errors.Is(err, unix.EINTR) {
			return err
		}
	}
	for {
		if err := check(); err != nil {
			return err
		}
		data, files, err := ipc.ReceiveFD(fd, 0)
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EINTR) {
			if _, err := unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}, 100); err != nil && !errors.Is(err, unix.EINTR) {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		ipc.CloseFiles(files)
		if len(files) != 0 || len(data) != 1 || data[0] != 'R' {
			return errors.New("invalid exec terminal readiness acknowledgement")
		}
		return check()
	}
}

// readExecMembers uses the retained host cgroup descriptor; the container root
// has no path to this cgroup or access to the descriptor.
func readExecMembers(group int) (map[int]bool, error) {
	fd, err := unix.Openat(group, "cgroup.procs", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "exec-cgroup-members")
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return nil, errors.New("cannot read bounded exec cgroup membership")
	}
	members := make(map[int]bool)
	for _, value := range strings.Fields(string(data)) {
		pid, err := strconv.Atoi(value)
		if err != nil || pid <= 0 {
			return nil, errors.New("invalid exec cgroup member")
		}
		members[pid] = true
	}
	return members, nil
}

func forwardExecTerminalSignal(master *os.File, group int, mainGroup int, sig syscall.Signal) {
	if sig == syscall.SIGINT || sig == syscall.SIGQUIT {
		// TIOCSIG holds the kernel's foreground-group reference while
		// signaling, avoiding a lookup through a reusable numeric PGID.
		if unix.IoctlSetInt(terminalFD(master), unix.TIOCSIG, int(sig)) == nil {
			return
		}
	}
	foreground, err := unix.IoctlGetInt(terminalFD(master), unix.TIOCGPGRP)
	if err != nil || foreground <= 0 {
		_ = unix.Kill(-mainGroup, sig) // Our main child has not been reaped.
		return
	}
	members, err := readExecMembers(group)
	if err != nil {
		return
	}
	deadline := time.Now().Add(time.Second)
	for pid := range members {
		if time.Now().After(deadline) {
			return
		}
		pidfd, err := unix.PidfdOpen(pid, 0)
		if err != nil {
			continue
		}
		current, err := readExecMembers(group)
		if err == nil && current[pid] {
			if pgid, err := unix.Getpgid(pid); err == nil && pgid == foreground {
				_ = unix.PidfdSendSignal(pidfd, sig, nil, 0)
			}
		}
		unix.Close(pidfd)
	}
}
