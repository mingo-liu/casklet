//go:build linux

package integration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

type terminalPTY struct {
	master, slave *os.File
	masterFD      int
	number        uint32
}

func openTerminalPTY(t *testing.T) terminalPTY {
	t.Helper()
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	master := os.NewFile(uintptr(fd), "terminal-test-master")
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		master.Close()
		t.Fatal(err)
	}
	number, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		master.Close()
		t.Fatal(err)
	}
	slave, err := os.OpenFile("/dev/pts/"+strconv.Itoa(number), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		master.Close()
		t.Fatal(err)
	}
	// Keep raw descriptor reads nonblocking, independent of os.File's poller.
	if err := unix.SetNonblock(fd, true); err != nil {
		master.Close()
		slave.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = slave.Close()
		_ = master.Close()
	})
	return terminalPTY{master: master, slave: slave, masterFD: fd, number: uint32(number)}
}

type terminalInvocation struct {
	pty      terminalPTY
	cmd      *exec.Cmd
	unit     string
	before   unix.Termios
	stderr   *os.File
	cancel   context.CancelFunc
	done     chan error
	readStop chan struct{}
	readDone chan struct{}
	stopOnce sync.Once
	mu       sync.Mutex
	output   []byte
	readErr  error
	waited   bool
}

func startTerminal(t *testing.T, options []string, command ...string) *terminalInvocation {
	t.Helper()
	require(t)
	pty := openTerminalPTY(t)
	before, err := unix.IoctlGetTermios(int(pty.slave.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.IoctlSetWinsize(int(pty.slave.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 24, Col: 80}); err != nil {
		t.Fatal(err)
	}
	stderr, err := os.CreateTemp(t.TempDir(), "terminal-stderr-")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	unit := fmt.Sprintf("mini-docker-test-%d-%d.scope", os.Getpid(), sequence.Add(1))
	args := []string{"--scope", "--quiet", "--unit=" + unit, "--property=Delegate=cpu memory pids", "--", binary, "run", "--rootfs", template, "-it"}
	args = append(args, options...)
	args = append(args, "--")
	args = append(args, command...)
	cmd := exec.CommandContext(ctx, "systemd-run", args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = pty.slave, pty.slave, stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	call := &terminalInvocation{pty: pty, cmd: cmd, unit: unit, before: *before, stderr: stderr, cancel: cancel, done: make(chan error, 1), readStop: make(chan struct{}), readDone: make(chan struct{})}
	t.Cleanup(func() {
		cancel()
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = exec.CommandContext(stopCtx, "systemctl", "stop", unit).Run()
		if !call.waited && cmd.Process != nil {
			select {
			case <-call.done:
			case <-time.After(5 * time.Second):
				t.Error("terminal launcher failed to terminate during cleanup")
			}
		}
		call.stopReader()
		_ = stderr.Close()
	})
	go call.readOutput()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { call.done <- cmd.Wait() }()
	return call
}

func (call *terminalInvocation) readOutput() {
	defer close(call.readDone)
	buf := make([]byte, 32*1024)
	stopping := false
	for {
		select {
		case <-call.readStop:
			stopping = true
		default:
		}
		n, err := unix.Read(call.pty.masterFD, buf)
		call.mu.Lock()
		if n > 0 {
			// A broken bounded helper must not exhaust the integration runner.
			if len(call.output)+n > 2<<20 {
				call.readErr = errors.New("terminal test output exceeds 2 MiB")
				call.mu.Unlock()
				return
			}
			call.output = append(call.output, buf[:n]...)
		}
		if err != nil && !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EINTR) && !errors.Is(err, unix.EIO) {
			call.readErr = err
			call.mu.Unlock()
			return
		}
		call.mu.Unlock()
		if n <= 0 {
			if stopping {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
}

func (call *terminalInvocation) stopReader() {
	call.stopOnce.Do(func() { close(call.readStop) })
	<-call.readDone
}

func (call *terminalInvocation) captured() (string, error) {
	call.mu.Lock()
	defer call.mu.Unlock()
	return string(call.output), call.readErr
}

func (call *terminalInvocation) outputContains(t *testing.T, expected string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		out, err := call.captured()
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out, expected) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	out, _ := call.captured()
	stderr, _ := os.ReadFile(call.stderr.Name())
	t.Fatalf("terminal output did not contain %q: stdout=%q stderr=%q", expected, out, stderr)
}

func (call *terminalInvocation) write(t *testing.T, value string) {
	t.Helper()
	remaining := []byte(value)
	deadline := time.Now().Add(3 * time.Second)
	for len(remaining) > 0 {
		n, err := unix.Write(call.pty.masterFD, remaining)
		if n > 0 {
			remaining = remaining[n:]
		}
		if err != nil && !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EINTR) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("terminal input write exceeded its deadline")
		}
		if n <= 0 {
			time.Sleep(5 * time.Millisecond)
		}
	}
}

func (call *terminalInvocation) wait(t *testing.T) (int, string, string) {
	t.Helper()
	var err error
	select {
	case err = <-call.done:
		call.waited = true
	case <-time.After(20 * time.Second):
		out, _ := call.captured()
		t.Fatalf("terminal launcher did not exit: %q", out)
	}
	call.cancel()
	code := 0
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("terminal launcher wait: %v", err)
		}
		code = exit.ExitCode()
	}
	// The held slave lets us verify restoration even after the launcher exits.
	after, err := unix.IoctlGetTermios(int(call.pty.slave.Fd()), unix.TCGETS)
	if err != nil || *after != call.before {
		t.Fatalf("host terminal was not restored: before=%+v after=%+v err=%v", call.before, after, err)
	}
	// The launcher has finished writing. Drain to EAGAIN before taking a snapshot.
	call.stopReader()
	out, readErr := call.captured()
	if readErr != nil {
		t.Fatal(readErr)
	}
	stderr, _ := os.ReadFile(call.stderr.Name())
	deadline := time.Now().Add(3 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		state, _ := exec.CommandContext(ctx, "systemctl", "is-active", call.unit).Output()
		cancel()
		active := strings.TrimSpace(string(state))
		if active != "active" && active != "deactivating" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("terminal scope remains populated: %s", call.unit)
		}
		time.Sleep(25 * time.Millisecond)
	}
	return code, out, string(stderr)
}

func (call *terminalInvocation) signal(t *testing.T, signal syscall.Signal) {
	t.Helper()
	pid := (&invocation{unit: call.unit}).supervisorPID()
	if pid == 0 {
		t.Fatal("terminal supervisor is missing from its delegated scope")
	}
	if err := syscall.Kill(pid, signal); err != nil {
		t.Fatal(err)
	}
}

func TestTerminalShellInteraction(t *testing.T) {
	call := startTerminal(t, nil, "/bin/sh", "-c", `
set -eu
test -t 0 && test -t 1 && test -t 2
printf 'terminal-ready\n' > /dev/tty
exec /bin/sh`)
	call.outputContains(t, "terminal-ready\r\n")
	active, err := unix.IoctlGetTermios(int(call.pty.slave.Fd()), unix.TCGETS)
	if err != nil || active.Lflag&(unix.ICANON|unix.ECHO|unix.ISIG) != 0 {
		t.Fatalf("host terminal did not enter raw mode: termios=%+v err=%v", active, err)
	}
	call.write(t, "printf 'INITIAL-SIZE:'; stty size\n")
	call.outputContains(t, "INITIAL-SIZE:24 80\r\n")
	if err := unix.IoctlSetWinsize(int(call.pty.slave.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 37, Col: 101}); err != nil {
		t.Fatal(err)
	}
	call.signal(t, syscall.SIGWINCH)
	call.write(t, `i=0; while [ "$(stty size)" != "37 101" ] && [ "$i" -lt 100 ]; do i=$((i+1)); sleep 0.02; done; printf 'UPDATED-SIZE:'; stty size`+"\n")
	call.outputContains(t, "UPDATED-SIZE:37 101\r\n")
	// The newline marker distinguishes command output from terminal echo.
	call.write(t, "printf 'SLEEP-STARTED\\n'; /bin/sleep 30\n")
	call.outputContains(t, "SLEEP-STARTED\r\n")
	time.Sleep(100 * time.Millisecond)
	call.write(t, "\x03")
	call.write(t, "printf 'AFTER-INT\\n'\n")
	call.outputContains(t, "AFTER-INT\r\n")
	call.write(t, "exit 7\n")
	code, out, stderr := call.wait(t)
	if code != 7 || strings.Contains(out, "can't access tty") || strings.Contains(out, "job control turned off") {
		t.Fatalf("interactive shell exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
}

func TestTerminalInteractiveEOF(t *testing.T) {
	call := startTerminal(t, nil, "/bin/sh", "-c", "printf 'cat-ready\\n'; exec /bin/cat")
	call.outputContains(t, "cat-ready\r\n")
	call.write(t, "interactive-input\n\x04")
	code, out, stderr := call.wait(t)
	if code != 0 || strings.Count(out, "interactive-input\r\n") != 2 {
		t.Fatalf("interactive stdin EOF exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
}

func TestTerminalExternalSignalTargetsForegroundJob(t *testing.T) {
	const prompt = "external-signal-prompt> "
	call := startTerminal(t, []string{"--env", "PS1=" + prompt}, "/bin/sh", "-c", "printf 'external-shell-ready\\n'; exec /bin/sh")
	call.outputContains(t, "external-shell-ready\r\n")
	call.outputContains(t, prompt)
	// The nested shell prints only after the interactive parent has made this
	// separate job its foreground process group. Its exec keeps that group.
	call.write(t, `/bin/sh -c 'printf "external-job-ready\n"; exec /bin/sleep 30'`+"\n")
	call.outputContains(t, "external-job-ready\r\n")
	before, _ := call.captured()
	prompts := strings.Count(before, prompt)
	started := time.Now()
	call.signal(t, syscall.SIGINT)
	// Ash discards the interrupted command line, so continuation belongs to
	// fresh input after its new prompt rather than a semicolon on the job's line.
	for {
		out, err := call.captured()
		if err != nil {
			t.Fatal(err)
		}
		if strings.Count(out, prompt) > prompts {
			break
		}
		if time.Since(started) > 3*time.Second {
			t.Fatalf("external SIGINT did not release foreground job: %q", out)
		}
		time.Sleep(10 * time.Millisecond)
	}
	call.write(t, `printf 'AFTER-EXTERNAL-INT\n'; exit 41`+"\n")
	code, out, stderr := call.wait(t)
	if code != 41 || !strings.Contains(out, "AFTER-EXTERNAL-INT\r\n") {
		t.Fatalf("external SIGINT did not reach foreground job: exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("external SIGINT waited for forced shutdown: %v", elapsed)
	}
}

func TestTerminalPrivateDevptsAndNonrootReadOnly(t *testing.T) {
	require(t)
	// Hold two host PTYs so the marker cannot coincide with the first private PTY.
	_ = openTerminalPTY(t)
	marker := openTerminalPTY(t)
	call := startTerminal(t, []string{"--user", "1000:1234", "--read-only"}, "/bin/sh", "-c", `
set -eu
[ "$(id -u)" = 1000 ] && [ "$(id -g)" = 1234 ]
test -t 0 && test -t 1 && test -t 2
[ "$(tty)" = /dev/pts/0 ]
[ -c /dev/pts/ptmx ] && [ -c /dev/ptmx ] && [ -c /dev/tty ]
[ ! -e "/dev/pts/$1" ]
awk '$2 == "/dev/pts" && $3 == "devpts" {print $4}' /proc/mounts | grep -q 'max=64'
printf 'private-tty-ready\n' > /dev/tty
read value < /dev/tty
[ "$value" = private-input ]
printf usable > /tmp/terminal-scratch
if (printf forbidden > /terminal-root-write) 2>/dev/null; then exit 1; fi
printf 'private-tty-ok\n'`, "sh", strconv.Itoa(int(marker.number)))
	call.outputContains(t, "private-tty-ready\r\n")
	call.write(t, "private-input\n")
	code, out, stderr := call.wait(t)
	if code != 0 || !strings.Contains(out, "private-tty-ok\r\n") {
		t.Fatalf("private nonroot read-only PTY exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
}

func TestTerminalRestorationAndExitCodes(t *testing.T) {
	for _, test := range []struct {
		name, command string
		options       []string
		code          int
	}{
		{"command-exit", "echo ready; exit 19", nil, 19},
		{"timeout", "echo ready; sleep 30", []string{"--timeout", "300ms"}, 124},
	} {
		t.Run(test.name, func(t *testing.T) {
			call := startTerminal(t, test.options, "/bin/sh", "-c", test.command)
			code, out, stderr := call.wait(t)
			if code != test.code || !strings.Contains(out, "ready\r\n") {
				t.Fatalf("terminal %s exit=%d stdout=%q stderr=%q", test.name, code, out, stderr)
			}
		})
	}
	t.Run("signal", func(t *testing.T) {
		call := startTerminal(t, nil, "/bin/sh", "-c", "trap 'echo stopped; exit 23' TERM; echo ready; while :; do sleep 1; done")
		call.outputContains(t, "ready\r\n")
		call.signal(t, syscall.SIGTERM)
		code, out, stderr := call.wait(t)
		if code != 23 || !strings.Contains(out, "stopped\r\n") {
			t.Fatalf("terminal SIGTERM exit=%d stdout=%q stderr=%q", code, out, stderr)
		}
	})
	for _, signal := range []syscall.Signal{syscall.SIGHUP, syscall.SIGQUIT} {
		t.Run(signal.String(), func(t *testing.T) {
			call := startTerminal(t, nil, "/bin/sh", "-c", "echo ready; exec /bin/sleep 30")
			call.outputContains(t, "ready\r\n")
			call.signal(t, signal)
			code, out, stderr := call.wait(t)
			if code != 128+int(signal) {
				t.Fatalf("terminal %s exit=%d stdout=%q stderr=%q", signal, code, out, stderr)
			}
		})
	}
	t.Run("command-startup-failure", func(t *testing.T) {
		call := startTerminal(t, nil, "/bin/does-not-exist")
		code, out, stderr := call.wait(t)
		if code != 125 {
			t.Fatalf("terminal startup failure exit=%d stdout=%q stderr=%q", code, out, stderr)
		}
	})
	t.Run("rootfs-startup-failure", func(t *testing.T) {
		call := startTerminal(t, []string{"--rootfs", "/does-not-exist"}, "/bin/echo", "must-not-run")
		code, out, stderr := call.wait(t)
		if code != 125 || strings.Contains(out, "must-not-run") {
			t.Fatalf("terminal missing rootfs exit=%d stdout=%q stderr=%q", code, out, stderr)
		}
	})
}

func TestTerminalInputModes(t *testing.T) {
	t.Run("interactive-requires-terminal", func(t *testing.T) {
		code, out, stderr := start(t, "must-not-forward\n", "run", "--rootfs", template, "-it", "--", "/bin/cat").wait(t)
		if code != 125 || out != "" || !strings.Contains(strings.ToLower(stderr), "terminal") {
			t.Fatalf("nonterminal interactive PTY exit=%d stdout=%q stderr=%q", code, out, stderr)
		}
	})
	t.Run("tty-without-input", func(t *testing.T) {
		code, out, stderr := start(t, "must-not-forward\n", "run", "--rootfs", template, "-t", "--", "/bin/sh", "-c", `
set -eu
test -t 0 && test -t 1 && test -t 2
if read value; then exit 1; fi
printf 'tty-stdout\n'
printf 'tty-stderr\n' >&2`).wait(t)
		if code != 0 || out != "tty-stdout\r\ntty-stderr\r\n" || stderr != "" {
			t.Fatalf("output-only TTY exit=%d stdout=%q stderr=%q", code, out, stderr)
		}
	})
	t.Run("disabled-input", func(t *testing.T) {
		code, out, stderr := start(t, "must-not-forward\n", "run", "--rootfs", template, "--interactive=false", "--", "/bin/cat").wait(t)
		if code != 0 || out != "" {
			t.Fatalf("disabled input exit=%d stdout=%q stderr=%q", code, out, stderr)
		}
	})
}

func TestTerminalOutputDrainAndCleanup(t *testing.T) {
	require(t)
	// A fast exiting command writes more than the PTY's small kernel buffer.
	// The last marker must survive every repeated startup and teardown.
	for i := 0; i < 4; i++ {
		call := startTerminal(t, nil, "/bin/sh", "-c", `
dd if=/dev/zero bs=4096 count=32 2>/dev/null | tr '\000' x
printf '\nterminal-output-end\n'
printf 'merged-stderr-end\n' >&2`)
		code, out, stderr := call.wait(t)
		if code != 0 || strings.Count(out, "x") != 128*1024 || !strings.HasSuffix(out, "terminal-output-end\r\nmerged-stderr-end\r\n") {
			t.Fatalf("terminal output drain iteration=%d exit=%d bytes=%d stderr=%q", i, code, len(out), stderr)
		}
	}
	entries, err := os.ReadDir("/var/lib/mini-docker/runs")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			t.Errorf("terminal run leaked runtime directory %s", entry.Name())
		}
	}
}
