//go:build linux

package runtime

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/mingo-liu/casklet/internal/config"
	"golang.org/x/sys/unix"
)

// prepareTerminal runs after pivot_root, while init still has mount privileges.
// Each container gets its own bounded devpts instance, never the host's PTYs.
func prepareTerminal(user *config.User) (*os.File, *os.File, error) {
	if err := prepareTerminalMounts(); err != nil {
		return nil, nil, err
	}
	return openTerminal(user)
}

// Managed containers prepare these mounts before dropping privileges so later
// exec sessions can allocate PTYs in the same private, bounded instance.
func prepareTerminalMounts() error {
	if err := os.Mkdir("/dev/pts", 0755); err != nil {
		return err
	}
	if err := unix.Mount("devpts", "/dev/pts", "devpts", unix.MS_NOSUID|unix.MS_NOEXEC, "newinstance,ptmxmode=0666,mode=0600,max=64"); err != nil {
		return fmt.Errorf("mount private devpts: %w", err)
	}
	if err := os.Symlink("pts/ptmx", "/dev/ptmx"); err != nil {
		return err
	}
	return nil
}

// openTerminal allocates a session without modifying existing terminal mounts.
func openTerminal(user *config.User) (*os.File, *os.File, error) {
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	master := os.NewFile(uintptr(fd), "container-pty")
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		master.Close()
		return nil, nil, err
	}
	number, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		master.Close()
		return nil, nil, err
	}
	slave, err := os.OpenFile("/dev/pts/"+strconv.Itoa(number), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		master.Close()
		return nil, nil, err
	}
	if user != nil {
		if err := slave.Chown(int(user.UID), int(user.GID)); err != nil {
			master.Close()
			slave.Close()
			return nil, nil, err
		}
	}
	return master, slave, nil
}

// SyscallConn preserves nonblocking mode; File.Fd may switch it to blocking.
func terminalFD(file *os.File) int {
	connection, err := file.SyscallConn()
	if err != nil {
		return -1
	}
	fd := -1
	_ = connection.Control(func(value uintptr) { fd = int(value) })
	return fd
}

func sendTerminal(socket int, master *os.File) error {
	return unix.Sendmsg(socket, []byte{'T'}, unix.UnixRights(terminalFD(master)), nil, 0)
}

func receiveTerminal(socket int) (*os.File, error) {
	data, ancillary := make([]byte, 1), make([]byte, unix.CmsgSpace(4))
	n, oobn, flags, _, err := unix.Recvmsg(socket, data, ancillary, unix.MSG_CMSG_CLOEXEC)
	if err != nil {
		return nil, err
	}
	messages, err := unix.ParseSocketControlMessage(ancillary[:oobn])
	if err != nil {
		return nil, err
	}
	var descriptors []int
	for _, message := range messages {
		fds, err := unix.ParseUnixRights(&message)
		if err != nil {
			for _, fd := range descriptors {
				unix.Close(fd)
			}
			return nil, err
		}
		descriptors = append(descriptors, fds...)
	}
	if n != 1 || data[0] != 'T' || flags&(unix.MSG_CTRUNC|unix.MSG_TRUNC) != 0 || len(descriptors) != 1 {
		for _, fd := range descriptors {
			unix.Close(fd)
		}
		return nil, errors.New("invalid terminal descriptor message")
	}
	fd := descriptors[0]
	if _, err := unix.IoctlGetInt(fd, unix.TIOCGPTN); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("received descriptor is not a PTY master: %w", err)
	}
	if err := unix.SetNonblock(fd, true); err != nil {
		unix.Close(fd)
		return nil, err
	}
	return os.NewFile(uintptr(fd), "container-pty"), nil
}

type terminalBridge struct {
	master, input, output *os.File
	hostInput             *os.File
	original              *unix.Termios
	done                  chan struct{}
	outputDone            chan struct{}
	errors                chan error
	workers               sync.WaitGroup
	windows               chan os.Signal
}

// reopenTerminalIO gives each copier its own nonblocking file description.
// Unlike dup plus F_SETFL, this never changes the caller's descriptor flags.
func reopenTerminalIO(file *os.File, mode int) (*os.File, error) {
	fd, err := unix.Open("/proc/self/fd/"+strconv.Itoa(terminalFD(file)), mode|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOCTTY, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), "terminal-io"), nil
}

func startTerminalBridge(master, stdin, stdout *os.File, interactive bool) (*terminalBridge, error) {
	b := &terminalBridge{master: master, hostInput: stdin, done: make(chan struct{}), outputDone: make(chan struct{}), errors: make(chan error, 3), windows: make(chan os.Signal, 1)}
	// Regular files retain the caller's offset; pipes and terminals need a
	// separately opened nonblocking description for cancellable output writes.
	info, err := stdout.Stat()
	if err != nil {
		return nil, err
	}
	if info.Mode().IsRegular() {
		b.output = stdout
	} else {
		b.output, err = reopenTerminalIO(stdout, unix.O_WRONLY)
		if err != nil {
			return nil, err
		}
	}
	if interactive {
		b.original, err = unix.IoctlGetTermios(terminalFD(stdin), unix.TCGETS)
		if err == nil {
			b.input, err = reopenTerminalIO(stdin, unix.O_RDONLY)
		}
		if err != nil {
			if b.output != stdout {
				b.output.Close()
			}
			return nil, fmt.Errorf("interactive TTY requires terminal stdin: %w", err)
		}
		raw := rawTermios(*b.original)
		if err := unix.IoctlSetTermios(terminalFD(stdin), unix.TCSETS, &raw); err != nil {
			b.input.Close()
			if b.output != stdout {
				b.output.Close()
			}
			return nil, fmt.Errorf("enter terminal raw mode: %w", err)
		}
	}
	if size, err := unix.IoctlGetWinsize(terminalFD(stdin), unix.TIOCGWINSZ); err == nil {
		if err := unix.IoctlSetWinsize(terminalFD(master), unix.TIOCSWINSZ, size); err != nil {
			b.close(stdout)
			return nil, err
		}
	} else {
		_ = unix.IoctlSetWinsize(terminalFD(master), unix.TIOCSWINSZ, &unix.Winsize{Row: 24, Col: 80})
	}
	signal.Notify(b.windows, syscall.SIGWINCH)
	b.workers.Add(2)
	go func() {
		defer b.workers.Done()
		defer close(b.outputDone)
		b.report(b.copy(b.output, b.master, true))
	}()
	go func() {
		defer b.workers.Done()
		for {
			select {
			case <-b.windows:
				if size, err := unix.IoctlGetWinsize(terminalFD(b.hostInput), unix.TIOCGWINSZ); err == nil {
					b.report(unix.IoctlSetWinsize(terminalFD(b.master), unix.TIOCSWINSZ, size))
				}
			case <-b.done:
				return
			}
		}
	}()
	if interactive {
		b.workers.Add(1)
		go func() {
			defer b.workers.Done()
			b.report(b.copy(b.master, b.input, false))
		}()
	} else {
		// With no attached input, present terminal EOF to a canonical reader.
		_, _ = master.Write([]byte{4})
	}
	return b, nil
}

func rawTermios(term unix.Termios) unix.Termios {
	term.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
	term.Oflag &^= unix.OPOST
	term.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	term.Cflag &^= unix.CSIZE | unix.PARENB
	term.Cflag |= unix.CS8
	term.Cc[unix.VMIN], term.Cc[unix.VTIME] = 1, 0
	return term
}

func (b *terminalBridge) report(err error) {
	if err != nil && !errors.Is(err, os.ErrClosed) {
		select {
		case b.errors <- err:
		case <-b.done:
		}
	}
}

func (b *terminalBridge) copy(destination, source *os.File, ptyOutput bool) error {
	buffer := make([]byte, 32*1024)
	for {
		if err := b.poll(terminalFD(source), unix.POLLIN); err != nil {
			return err
		}
		n, err := unix.Read(terminalFD(source), buffer)
		if errors.Is(err, unix.EINTR) || errors.Is(err, unix.EAGAIN) {
			continue
		}
		if ptyOutput && errors.Is(err, unix.EIO) {
			return nil // Last slave closed; Linux reports terminal EOF as EIO.
		}
		if err != nil || n == 0 {
			return err
		}
		for written := 0; written < n; {
			if err := b.poll(terminalFD(destination), unix.POLLOUT); err != nil {
				return err
			}
			count, err := unix.Write(terminalFD(destination), buffer[written:n])
			if errors.Is(err, unix.EINTR) || errors.Is(err, unix.EAGAIN) {
				continue
			}
			if err != nil {
				return err
			}
			if count == 0 {
				return io.ErrShortWrite
			}
			written += count
		}
	}
}

func (b *terminalBridge) poll(fd int, events int16) error {
	for {
		select {
		case <-b.done:
			return os.ErrClosed
		default:
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

func (b *terminalBridge) close(stdout *os.File) error {
	// Let trailing output drain after all slaves close, but never let a blocked
	// consumer prevent container cleanup or restoration of the host terminal.
	select {
	case <-b.outputDone:
	case <-time.After(time.Second):
	}
	close(b.done)
	signal.Stop(b.windows)
	b.workers.Wait()
	b.master.Close()
	if b.input != nil {
		b.input.Close()
	}
	if b.output != stdout {
		b.output.Close()
	}
	if b.original != nil {
		if err := unix.IoctlSetTermios(terminalFD(b.hostInput), unix.TCSETS, b.original); err != nil {
			return fmt.Errorf("restore terminal: %w", err)
		}
	}
	return nil
}
