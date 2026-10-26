package machine

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"testing"
	"time"
)

func TestGuestSessionHelper(t *testing.T) {
	mode := os.Getenv("CASKLET_TEST_GUEST_SESSION")
	if mode == "" {
		return
	}
	signals := make(chan os.Signal, 8)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP, syscall.SIGQUIT, syscall.SIGWINCH)
	fmt.Println("ready")
	for {
		select {
		case sig := <-signals:
			if mode == "responsive" && sig != syscall.SIGWINCH {
				os.Exit(9)
			}
		case <-time.After(10 * time.Second):
			os.Exit(99)
		}
	}
}

func guestSessionCommand(t *testing.T, mode string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestGuestSessionHelper$")
	cmd.Env = append(os.Environ(), "CASKLET_TEST_GUEST_SESSION="+mode)
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); output.Close() })
	if line, err := bufio.NewReader(output).ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("helper startup: %q, %v", line, err)
	}
	return cmd
}

func TestGuestSessionContextCancellationNotifiesAndReaps(t *testing.T) {
	for _, responsive := range []bool{true, false} {
		t.Run(fmt.Sprint(responsive), func(t *testing.T) {
			mode := "unresponsive"
			if responsive {
				mode = "responsive"
			}
			cmd := guestSessionCommand(t, mode)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			calls := 0
			started := time.Now()
			interrupted, err := waitGuest(ctx, cmd, nil, 100*time.Millisecond, func(sig syscall.Signal) {
				calls++
				if sig != syscall.SIGTERM {
					t.Fatalf("forwarded %v instead of SIGTERM", sig)
				}
				if responsive {
					_ = cmd.Process.Signal(sig)
				}
			})
			if !errors.Is(err, context.Canceled) || interrupted != 0 || calls != 1 || cmd.ProcessState == nil {
				t.Fatalf("cancellation: interrupted=%v err=%v calls=%d state=%v", interrupted, err, calls, cmd.ProcessState)
			}
			if time.Since(started) > 2*time.Second {
				t.Fatal("cancellation did not bound the transport lifetime")
			}
			if responsive && cmd.ProcessState.ExitCode() != 9 {
				t.Fatalf("remote cleanup was cut short: %v", cmd.ProcessState)
			}
		})
	}
}

func TestGuestSessionDeadlineAndFailedForwardingRemainBounded(t *testing.T) {
	cmd := guestSessionCommand(t, "unresponsive")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	calls := 0
	_, err := waitGuest(ctx, cmd, nil, 50*time.Millisecond, func(syscall.Signal) { calls++ })
	if !errors.Is(err, context.DeadlineExceeded) || calls != 1 || cmd.ProcessState == nil {
		t.Fatalf("deadline: %v calls=%d state=%v", err, calls, cmd.ProcessState)
	}
}

func TestGuestSessionSignalsPreserveCommandExitAndArchiveIdentity(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT} {
		t.Run(sig.String(), func(t *testing.T) {
			cmd := guestSessionCommand(t, "responsive")
			signals := make(chan os.Signal, 2)
			signals <- syscall.SIGWINCH
			signals <- sig
			close(signals)
			calls := 0
			interrupted, err := waitGuest(context.Background(), cmd, signals, time.Second, func(got syscall.Signal) {
				calls++
				if got != sig {
					t.Fatalf("forwarded %v, want %v", got, sig)
				}
				_ = cmd.Process.Signal(got)
			})
			var exit *exec.ExitError
			if interrupted != sig || !errors.As(err, &exit) || exit.ExitCode() != 9 || calls != 1 {
				t.Fatalf("signal: interrupted=%v err=%v calls=%d", interrupted, err, calls)
			}
		})
	}
}

func TestGuestSessionSignalTimeoutReapsTransport(t *testing.T) {
	cmd := guestSessionCommand(t, "unresponsive")
	signals := make(chan os.Signal, 1)
	signals <- syscall.SIGINT
	interrupted, err := waitGuest(context.Background(), cmd, signals, 20*time.Millisecond, func(syscall.Signal) {})
	if interrupted != syscall.SIGINT || err == nil || cmd.ProcessState == nil {
		t.Fatalf("signal timeout: %v %v %v", interrupted, err, cmd.ProcessState)
	}
}

func TestCanceledInvocationStopsBeforeMachinePreparation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	code, err := Execute(ctx, Invocation{Args: []string{"run", "--rootfs", "/missing", "--", "true"}}, nil, nil, nil)
	if code != 125 || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled invocation: code=%d err=%v", code, err)
	}
}
