//go:build linux

package runtime

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mingo-liu/casklet/internal/config"
	"golang.org/x/sys/unix"
)

func TestExecConfigurationIsImmutableAndOffsetIndependent(t *testing.T) {
	file, err := sealExecConfig(config.Config{Command: []string{"/bin/true"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if _, err := file.WriteString("changed"); !errors.Is(err, syscall.EPERM) {
		t.Fatalf("write sealed configuration: %v", err)
	}
	if err := file.Truncate(0); !errors.Is(err, syscall.EPERM) {
		t.Fatalf("truncate sealed configuration: %v", err)
	}
	if os.Geteuid() != 0 {
		if _, err := readExecConfig(int(file.Fd())); err == nil {
			t.Fatal("non-root configuration was accepted")
		}
		return
	}
	for i := 0; i < 2; i++ {
		cfg, err := readExecConfig(int(file.Fd()))
		if err != nil || len(cfg.Command) != 1 || cfg.Command[0] != "/bin/true" {
			t.Fatalf("read configuration %d: %#v, %v", i, cfg, err)
		}
	}
}

func TestExecConfigurationRejectsOversizedPayload(t *testing.T) {
	_, err := sealExecConfig(config.Config{Command: []string{"/bin/echo", strings.Repeat("x", _execConfigLimit)}})
	if err == nil {
		t.Fatal("oversized configuration was accepted")
	}
}

func TestExecConfigurationRejectsUnsealedAndMalformedPayloads(t *testing.T) {
	tests := []struct {
		name, data string
		sealed     bool
	}{
		{"unsealed", `{"command":["/bin/true"]}`, false},
		{"unknown field", `{"command":["/bin/true"],"unexpected":true}`, true},
		{"trailing data", `{"command":["/bin/true"]} {}`, true},
		{"missing command", `{}`, true},
		{"negative timeout", `{"command":["/bin/true"],"timeout":-1}`, true},
		{"malformed", `{`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fd, err := unix.MemfdCreate("exec-test", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
			if err != nil {
				t.Fatal(err)
			}
			file := os.NewFile(uintptr(fd), "exec-test")
			defer func() { _ = file.Close() }()
			if _, err := file.WriteString(tt.data); err != nil {
				t.Fatal(err)
			}
			if tt.sealed {
				if _, err := unix.FcntlInt(file.Fd(), unix.F_ADD_SEALS, _execConfigSeals); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := readExecConfig(fd); err == nil {
				t.Fatal("invalid configuration was accepted")
			}
		})
	}
}

func TestExecuteRequiresLiveResources(t *testing.T) {
	code, err := ExecuteInContainer(context.Background(), ExecResources{}, config.Exec{Command: []string{"/bin/true"}}, nil, nil, nil, nil, nil)
	if code != 125 || err == nil {
		t.Fatalf("missing execution resources: code=%d, error=%v", code, err)
	}
}

func TestExecuteTTYRequiresTerminalClient(t *testing.T) {
	code, err := ExecuteInContainer(context.Background(), ExecResources{}, config.Exec{Command: []string{"/bin/true"}, TTY: true}, nil, nil, nil, nil, nil)
	if code != 125 || err == nil || !strings.Contains(err.Error(), "terminal client") {
		t.Fatalf("missing TTY client: code=%d, error=%v", code, err)
	}
}

func TestExecTerminalWaitsForConfiguredClient(t *testing.T) {
	master, _ := testPTY(t)
	parent, helper := execTerminalTestPair(t)
	signals := make(chan os.Signal, 1)
	authorized := make(chan error, 1)
	go func() { authorized <- authorizeExecTerminal(terminalFD(helper), master, signals) }()
	ready, release := make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- publishExecTerminal(ctx, parent, func(ctx context.Context, received *os.File) error {
			if err := unix.IoctlSetWinsize(terminalFD(received), unix.TIOCSWINSZ, &unix.Winsize{Row: 51, Col: 113}); err != nil {
				return err
			}
			close(ready)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal("terminal callback did not receive the PTY")
	}
	select {
	case err := <-authorized:
		t.Fatalf("helper started before client readiness: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	for _, done := range []<-chan error{result, authorized} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal("terminal readiness handshake did not complete")
		}
	}
	size, err := unix.IoctlGetWinsize(terminalFD(master), unix.TIOCGWINSZ)
	if err != nil || size.Row != 51 || size.Col != 113 {
		t.Fatalf("initial terminal size: %+v, %v", size, err)
	}
}

func TestExecTerminalCancellation(t *testing.T) {
	parent, _ := execTerminalTestPair(t)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- publishExecTerminal(ctx, parent, func(context.Context, *os.File) error {
			t.Error("callback ran before a master was received")
			return nil
		})
	}()
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled terminal handshake: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("terminal handshake ignored cancellation")
	}
}

func TestExecTerminalClientFailureDoesNotAuthorizeCommand(t *testing.T) {
	master, _ := testPTY(t)
	parent, helper := execTerminalTestPair(t)
	signals := make(chan os.Signal, 1)
	result := make(chan error, 1)
	go func() { result <- authorizeExecTerminal(terminalFD(helper), master, signals) }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	failure := errors.New("terminal client failed")
	err := publishExecTerminal(ctx, parent, func(context.Context, *os.File) error { return failure })
	if !errors.Is(err, failure) {
		t.Fatalf("terminal callback failure: %v", err)
	}
	select {
	case err := <-result:
		t.Fatalf("failed client authorized command: %v", err)
	default:
	}
	signals <- syscall.SIGTERM
	select {
	case err := <-result:
		var stopped execTerminalStoppedError
		if !errors.As(err, &stopped) || stopped.signal != syscall.SIGTERM {
			t.Fatalf("terminal setup ignored stop signal: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("terminal helper ignored stop during setup")
	}
}

func TestExecTerminalInterruptTargetsForegroundGroup(t *testing.T) {
	const helper = "CASKLET_TEST_FOREGROUND_INTERRUPT"
	if os.Getenv(helper) == "1" {
		interrupts := make(chan os.Signal, 1)
		signal.Notify(interrupts, syscall.SIGINT)
		ready := os.NewFile(3, "foreground-ready")
		if _, err := ready.Write([]byte{1}); err != nil {
			t.Fatal(err)
		}
		_ = ready.Close()
		<-interrupts
		os.Exit(130)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var (
		master, slave      = testPTY(t)
		ready, writer, err = os.Pipe()
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ready.Close() }()
	defer func() { _ = writer.Close() }()
	// A background shell can pass SIG_IGN for SIGINT through exec. Install
	// the child's handler explicitly and wait until it is ready before signaling.
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestExecTerminalInterruptTargetsForegroundGroup$")
	command.Env = append(os.Environ(), helper+"=1")
	command.Stdin, command.Stdout, command.Stderr = slave, slave, slave
	command.ExtraFiles = []*os.File{writer}
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	done := make(chan struct{})
	go func() { _ = command.Wait(); close(done) }()
	defer func() { _ = command.Process.Kill(); <-done }()
	if err := ready.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var marker [1]byte
	if _, err := io.ReadFull(ready, marker[:]); err != nil {
		t.Fatalf("terminal foreground helper readiness: %v", err)
	}
	// A working TIOCSIG targets the terminal's actual group; it needs no
	// numeric PID lookup or cgroup descriptor from this unprivileged test.
	forwardExecTerminalSignal(master, -1, command.Process.Pid, syscall.SIGINT)
	select {
	case <-done:
		if code := execStatus(command.ProcessState); code != 130 {
			t.Fatalf("foreground interrupt exit=%d; want 130", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("terminal foreground group ignored SIGINT")
	}
}

func TestExecExitStatus(t *testing.T) {
	tests := []struct {
		command string
		code    int
	}{
		{"exit 0", 0},
		{"exit 7", 7},
		{"kill -TERM $$", 143},
	}
	for _, tt := range tests {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		command := exec.CommandContext(ctx, "/bin/sh", "-c", tt.command)
		_ = command.Run()
		cancel()
		if command.ProcessState == nil || execStatus(command.ProcessState) != tt.code {
			t.Fatalf("%q: expected %d, got %v", tt.command, tt.code, command.ProcessState)
		}
	}
}

func TestExecConfigurationPreservesLargeInheritedEnvironment(t *testing.T) {
	base := config.Config{Command: []string{"/bin/sleep", "30"}, Env: []string{"VALUE=" + strings.Repeat("x", 70<<10)}}
	cfg := (config.Exec{Command: []string{"/bin/true"}, Env: []string{"EXTRA=" + strings.Repeat("y", 70<<10)}}).Apply(base)
	if err := cfg.ValidateExecution(); err != nil {
		t.Fatal(err)
	}
	file, err := sealExecConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if os.Geteuid() != 0 {
		return
	}
	decoded, err := readExecConfig(int(file.Fd()))
	if err != nil || len(decoded.Env) != 2 || decoded.Env[0] != cfg.Env[0] || decoded.Env[1] != cfg.Env[1] {
		t.Fatalf("large inherited configuration did not round trip: %v", err)
	}
}

func execTerminalTestPair(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	left, right := os.NewFile(uintptr(pair[0]), "terminal-test-left"), os.NewFile(uintptr(pair[1]), "terminal-test-right")
	t.Cleanup(func() { _ = left.Close(); _ = right.Close() })
	return left, right
}
