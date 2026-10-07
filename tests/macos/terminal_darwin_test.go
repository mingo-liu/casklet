//go:build darwin

package macos

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

func openPTY(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	master := os.NewFile(uintptr(fd), "macos-test-master")
	t.Cleanup(func() { master.Close() })
	for _, operation := range []uint{unix.TIOCPTYGRANT, unix.TIOCPTYUNLK} {
		if err := unix.IoctlSetInt(fd, operation, 0); err != nil {
			t.Fatal(err)
		}
	}
	var name [128]byte
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), unix.TIOCPTYGNAME, uintptr(unsafe.Pointer(&name[0])))
	if errno != 0 {
		t.Fatal(errno)
	}
	slave, err := os.OpenFile(strings.TrimRight(string(name[:]), "\x00"), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { slave.Close() })
	return master, slave
}

func TestInteractiveRunAndExecResizeAndRestore(t *testing.T) {
	client(t)
	id := detached(t, "--", "/bin/sleep", "60")
	for _, args := range [][]string{{"run", "-it", "--"}, {"exec", "-it", id, "--"}} {
		t.Run(args[0], func(t *testing.T) {
			master, slave := openPTY(t)
			before, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TIOCGETA)
			if err != nil {
				t.Fatal(err)
			}
			if err := unix.IoctlSetWinsize(int(slave.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 32, Col: 105}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			command := append(append([]string(nil), args...), "/bin/sh", "-c", "stty size; echo tty-ready; read value; stty size; echo tty-input:$value")
			cmd := exec.CommandContext(ctx, client(t), command...)
			cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
			// Keep the test PTY owned by the parent. Darwin revokes a controlling
			// terminal when its session leader exits, preventing a restoration check.
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cmd.Process.Kill() })
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			output := ""
			readUntil := func(marker string) {
				t.Helper()
				deadline := time.Now().Add(10 * time.Second)
				for !strings.Contains(output, marker) && time.Now().Before(deadline) {
					poll := []unix.PollFd{{Fd: int32(master.Fd()), Events: unix.POLLIN}}
					if _, err := unix.Poll(poll, 100); err != nil && err != unix.EINTR {
						t.Fatal(err)
					}
					if poll[0].Revents&unix.POLLIN != 0 {
						var buffer [4096]byte
						n, err := unix.Read(int(master.Fd()), buffer[:])
						if err != nil && err != unix.EAGAIN {
							t.Fatal(err)
						}
						if n > 0 {
							output += string(buffer[:n])
						}
					}
				}
				if !strings.Contains(output, marker) {
					t.Fatalf("missing %q: %s", marker, output)
				}
			}
			readUntil("tty-ready")
			if err := unix.IoctlSetWinsize(int(slave.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 40, Col: 120}); err != nil {
				t.Fatal(err)
			}
			if err := cmd.Process.Signal(syscall.SIGWINCH); err != nil {
				t.Fatal(err)
			}
			// Wait for the SSH resize message before the command checks dimensions.
			time.Sleep(150 * time.Millisecond)
			if _, err := master.Write([]byte("hello-terminal\n")); err != nil {
				t.Fatal(err)
			}
			readUntil("tty-input:hello-terminal")
			// Darwin terminal teardown drains pending output before completing
			// process exit. Keep reading SSH's final output while waiting.
		waiting:
			for {
				select {
				case err := <-done:
					if err != nil {
						t.Fatalf("terminal exit: %v: %s", err, output)
					}
					break waiting
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				default:
					poll := []unix.PollFd{{Fd: int32(master.Fd()), Events: unix.POLLIN}}
					_, _ = unix.Poll(poll, 100)
					if poll[0].Revents&unix.POLLIN != 0 {
						var buffer [4096]byte
						n, _ := unix.Read(int(master.Fd()), buffer[:])
						if n > 0 {
							output += string(buffer[:n])
						}
					}
				}
			}
			if !strings.Contains(output, "32 105") || !strings.Contains(output, "40 120") {
				t.Fatalf("resize: %s", output)
			}
			after, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TIOCGETA)
			if err != nil || *before != *after {
				t.Fatal(fmt.Sprintf("host terminal was not restored: %v", err))
			}
		})
	}
}
