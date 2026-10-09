//go:build darwin

package macos

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mingo-liu/casklet/internal/container"
)

func TestWaitHealthyThroughMacTransport(t *testing.T) {
	id := detached(t, "--health-cmd", "test -f /ready", "--health-interval", "100ms", "--health-timeout", "1s", "--health-retries", "1", "--", "/bin/sleep", "300")
	macHealth(t, id, container.HealthUnhealthy)
	out, diagnostic, code := command(t, "wait", "--healthy", "--timeout", "100ms", id)
	if code != 124 || out != "" || !strings.Contains(diagnostic, "timed out waiting") {
		t.Fatalf("timeout: %d %q %q", code, out, diagnostic)
	}
	success(t, "exec", id, "--", "/bin/true") // Timing out does not stop the workload.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, client(t), "wait", "--healthy", "--timeout", "10s", id[:12])
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() { cancel(); <-done })
	select {
	case err := <-done:
		done <- err
		t.Fatalf("wait finished before readiness: %v", err)
	case <-time.After(500 * time.Millisecond):
	}
	success(t, "exec", id, "--", "/bin/touch", "/ready")
	select {
	case err := <-done:
		done <- err
		if err != nil || stdout.String() != "0\n" || stderr.Len() != 0 {
			t.Fatalf("readiness: %v %q %q", err, stdout.String(), stderr.String())
		}
	case <-ctx.Done():
		t.Fatal("readiness wait did not finish")
	}
	if out := success(t, "wait", "--healthy", id); out != "0\n" {
		t.Fatal(out)
	}
	success(t, "stop", id)
	out, diagnostic, code = command(t, "wait", "--healthy", id)
	if code != 125 || out != "" || !strings.Contains(diagnostic, "stopped before becoming healthy") {
		t.Fatalf("stopped: %d %q %q", code, out, diagnostic)
	}
	unchecked := detached(t, "--", "/bin/sleep", "300")
	out, diagnostic, code = command(t, "wait", "--healthy", unchecked)
	if code != 125 || out != "" || !strings.Contains(diagnostic, "no healthcheck") {
		t.Fatalf("missing check: %d %q %q", code, out, diagnostic)
	}
}

func TestWaitHealthyMacCancellationLeavesWorkloadRunning(t *testing.T) {
	id := detached(t, "--health-cmd", "false", "--health-interval", "100ms", "--health-retries", "1", "--", "/bin/sleep", "300")
	macHealth(t, id, container.HealthUnhealthy)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, client(t), "wait", "--healthy", "--timeout", "0s", id)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() { cancel(); <-done })
	// Let the guest signal session become active before interrupting the host.
	time.Sleep(time.Second)
	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		done <- err
		if cmd.ProcessState.ExitCode() != 130 || stdout.Len() != 0 {
			t.Fatalf("cancel: %v exit=%d stdout=%q stderr=%q", err, cmd.ProcessState.ExitCode(), stdout.String(), stderr.String())
		}
	case <-ctx.Done():
		t.Fatal("canceled wait did not finish")
	}
	success(t, "exec", id, "--", "/bin/true")
}
