//go:build linux

package runtime

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func testPTY(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NONBLOCK|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	master := os.NewFile(uintptr(fd), "test-pty-master")
	t.Cleanup(func() { _ = master.Close() })
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}
	number, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	slave, err := os.OpenFile(filepath.Join("/dev/pts", strconv.Itoa(number)), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = slave.Close() })
	return master, slave
}

func testTerminalSocket(t *testing.T) (int, int) {
	t.Helper()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Close(fds[0]); _ = unix.Close(fds[1]) })
	return fds[0], fds[1]
}

func TestRawTermiosPreservesUnrelatedSettings(t *testing.T) {
	original := unix.Termios{
		Iflag: unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON | unix.IGNPAR,
		Oflag: unix.OPOST | unix.ONLCR,
		Lflag: unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN | unix.NOFLSH,
		Cflag: unix.CS7 | unix.PARENB | unix.CREAD | unix.CLOCAL,
	}
	original.Cc[unix.VMIN], original.Cc[unix.VTIME], original.Cc[unix.VERASE] = 9, 8, 127
	got := rawTermios(original)
	if got.Iflag != unix.IGNPAR || got.Oflag != unix.ONLCR || got.Lflag != unix.NOFLSH || got.Cflag != unix.CS8|unix.CREAD|unix.CLOCAL {
		t.Fatalf("unexpected raw flags: %+v", got)
	}
	if got.Cc[unix.VMIN] != 1 || got.Cc[unix.VTIME] != 0 || got.Cc[unix.VERASE] != 127 {
		t.Fatalf("unexpected raw control characters: %v", got.Cc)
	}
	if original.Cc[unix.VMIN] != 9 || original.Cc[unix.VTIME] != 8 {
		t.Fatal("raw conversion changed the saved terminal state")
	}
}

func TestTerminalDescriptorTransfer(t *testing.T) {
	master, _ := testPTY(t)
	send, receive := testTerminalSocket(t)
	if err := sendTerminal(send, master); err != nil {
		t.Fatal(err)
	}
	transferred, err := receiveTerminal(receive)
	if err != nil {
		t.Fatal(err)
	}
	defer transferred.Close()
	fd := terminalFD(transferred)
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	if err != nil || flags&unix.FD_CLOEXEC == 0 {
		t.Fatalf("transferred descriptor is not close-on-exec: flags=%d error=%v", flags, err)
	}
	for _, file := range []*os.File{master, transferred} {
		flags, err = unix.FcntlInt(uintptr(terminalFD(file)), unix.F_GETFL, 0)
		if err != nil || flags&unix.O_NONBLOCK == 0 {
			t.Fatalf("terminalFD changed nonblocking mode: flags=%d error=%v", flags, err)
		}
	}
	if _, err := unix.IoctlGetInt(fd, unix.TIOCGPTN); err != nil {
		t.Fatalf("transferred descriptor is not usable: %v", err)
	}
}

func TestTerminalForegroundGroupRequiresMasterOutsideChildSession(t *testing.T) {
	master, slave := testPTY(t)
	child, err := os.StartProcess("/bin/sh", []string{"sh", "-c", "exec sleep 30"}, &os.ProcAttr{
		Env:   []string{"PATH=/bin:/usr/bin"},
		Files: []*os.File{slave, slave, slave},
		Sys:   &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Kill(); _, _ = child.Wait() }()
	group, err := unix.IoctlGetInt(terminalFD(master), unix.TIOCGPGRP)
	if err != nil || group != child.Pid {
		t.Fatalf("master foreground group = %d; want %d, error=%v", group, child.Pid, err)
	}
	if _, err := unix.IoctlGetInt(terminalFD(slave), unix.TIOCGPGRP); !errors.Is(err, unix.ENOTTY) {
		t.Fatalf("slave query outside controlling session = %v; want ENOTTY", err)
	}
}

func TestReceiveTerminalRejectsInvalidDescriptorsWithoutLeaks(t *testing.T) {
	master, slave := testPTY(t)
	pipeRead, pipeWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pipeRead.Close()
	defer pipeWrite.Close()
	for _, tt := range []struct {
		name string
		data []byte
		fds  []int
	}{
		{"no descriptor", []byte{'T'}, nil},
		{"wrong payload", []byte{'X'}, []int{terminalFD(master)}},
		{"truncated payload", []byte{'T', 'X'}, []int{terminalFD(master)}},
		{"pipe descriptor", []byte{'T'}, []int{terminalFD(pipeRead)}},
		{"slave descriptor", []byte{'T'}, []int{terminalFD(slave)}},
		{"multiple descriptors", []byte{'T'}, []int{terminalFD(master), terminalFD(master)}},
		{"truncated ancillary", []byte{'T'}, []int{terminalFD(master), terminalFD(master), terminalFD(master), terminalFD(master)}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			send, receive := testTerminalSocket(t)
			before, err := os.ReadDir("/proc/self/fd")
			if err != nil {
				t.Fatal(err)
			}
			var ancillary []byte
			if len(tt.fds) > 0 {
				ancillary = unix.UnixRights(tt.fds...)
			}
			if err := unix.Sendmsg(send, tt.data, ancillary, nil, 0); err != nil {
				t.Fatal(err)
			}
			file, err := receiveTerminal(receive)
			if err == nil {
				file.Close()
				t.Fatal("accepted invalid terminal message")
			}
			after, err := os.ReadDir("/proc/self/fd")
			if err != nil || len(after) != len(before) {
				t.Fatalf("descriptor leak: before=%d after=%d error=%v", len(before), len(after), err)
			}
		})
	}
}

func TestTerminalBridgeCancelsBlockedOutputAndRestoresTerminal(t *testing.T) {
	master, slave := testPTY(t)
	_, hostInput := testPTY(t)
	original, err := unix.IoctlGetTermios(terminalFD(hostInput), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	// Keep the reader open but never consume output, so the bridge's writer
	// must be cancellable even when the caller's pipe is completely full.
	var fds [2]int
	if err := unix.Pipe2(fds[:], unix.O_NONBLOCK|unix.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	reader, writer := os.NewFile(uintptr(fds[0]), "blocked-output-reader"), os.NewFile(uintptr(fds[1]), "blocked-output-writer")
	defer reader.Close()
	defer writer.Close()
	buffer := make([]byte, 4096)
	for {
		_, err := unix.Write(fds[1], buffer)
		if errors.Is(err, unix.EAGAIN) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	bridge, err := startTerminalBridge(master, hostInput, writer, true)
	if err != nil {
		t.Fatal(err)
	}
	active, err := unix.IoctlGetTermios(terminalFD(hostInput), unix.TCGETS)
	if err != nil || active.Lflag&(unix.ECHO|unix.ICANON|unix.ISIG) != 0 {
		_ = bridge.close(writer)
		t.Fatalf("terminal did not enter raw mode: %+v error=%v", active, err)
	}
	if _, err := slave.Write([]byte("blocked-output")); err != nil {
		_ = bridge.close(writer)
		t.Fatal(err)
	}
	started := time.Now()
	if err := bridge.close(writer); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("blocked output delayed terminal cleanup: %s", elapsed)
	}
	restored, err := unix.IoctlGetTermios(terminalFD(hostInput), unix.TCGETS)
	if err != nil || !reflect.DeepEqual(restored, original) {
		t.Fatalf("terminal state was not restored: got=%+v want=%+v error=%v", restored, original, err)
	}
	flags, err := unix.FcntlInt(uintptr(fds[1]), unix.F_GETFL, 0)
	if err != nil || flags&unix.O_NONBLOCK == 0 {
		t.Fatalf("caller output descriptor changed: flags=%d error=%v", flags, err)
	}
}

func TestTerminalBridgeRejectsPipeInputWithoutLeaks(t *testing.T) {
	master, _ := testPTY(t)
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	output, err := os.CreateTemp(t.TempDir(), "output-")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	before, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := startTerminalBridge(master, reader, output, true)
	if err == nil {
		_ = bridge.close(output)
		t.Fatal("interactive bridge accepted pipe stdin")
	}
	after, err := os.ReadDir("/proc/self/fd")
	if err != nil || len(after) != len(before) {
		t.Fatalf("startup descriptor leak: before=%d after=%d error=%v", len(before), len(after), err)
	}
}

func TestTerminalBridgeRestoresTerminalAfterStartupFailure(t *testing.T) {
	_, hostInput := testPTY(t)
	original, err := unix.IoctlGetTermios(terminalFD(hostInput), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	// A failed initial resize happens after raw mode is enabled. Restoration
	// must also cover this startup path before any copying goroutines exist.
	invalidMaster, err := os.OpenFile("/dev/null", os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer invalidMaster.Close()
	output, err := os.CreateTemp(t.TempDir(), "output-")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	bridge, err := startTerminalBridge(invalidMaster, hostInput, output, true)
	if err == nil {
		_ = bridge.close(output)
		t.Fatal("bridge accepted an invalid PTY master")
	}
	restored, err := unix.IoctlGetTermios(terminalFD(hostInput), unix.TCGETS)
	if err != nil || !reflect.DeepEqual(restored, original) {
		t.Fatalf("startup failure left raw mode enabled: got=%+v want=%+v error=%v", restored, original, err)
	}
}
