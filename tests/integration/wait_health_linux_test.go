//go:build linux

package integration

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

func TestWaitHealthyReadinessAndFailures(t *testing.T) {
	require(t)
	id := startBackground(t, backgroundName(t), []string{"--health-cmd", "test -f /ready", "--health-interval", "50ms", "--health-timeout", "500ms", "--health-retries", "1"}, "/bin/sleep", "300")
	awaitHealth(t, id, container.HealthUnhealthy)
	code, out, diagnostic := backgroundCLI(t, "wait", "--healthy", "--timeout", "100ms", id)
	if code != 124 || out != "" || !strings.Contains(diagnostic, "timed out waiting") {
		t.Fatalf("timeout: exit=%d stdout=%q stderr=%q", code, out, diagnostic)
	}
	waitBackground(t, id, "running")
	// A delayed transition must keep the wait pending across unhealthy samples.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "wait", "--healthy", "--timeout", "5s", id[:12])
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
		t.Fatalf("wait completed while unhealthy: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	backgroundSuccess(t, "exec", id, "--", "/bin/touch", "/ready")
	select {
	case err := <-done:
		done <- err
		if err != nil || stdout.String() != "0\n" || stderr.Len() != 0 {
			t.Fatalf("readiness: %v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
		}
	case <-ctx.Done():
		t.Fatal("readiness wait did not finish")
	}
	if out := backgroundSuccess(t, "wait", "--healthy", id); out != "0\n" {
		t.Fatal(out)
	}
	backgroundSuccess(t, "stop", id)
	code, out, diagnostic = backgroundCLI(t, "wait", "--healthy", id)
	if code != 125 || out != "" || !strings.Contains(diagnostic, "stopped before becoming healthy") {
		t.Fatalf("stopped: %d %q %q", code, out, diagnostic)
	}
	unchecked := startBackground(t, backgroundName(t), nil, "/bin/sleep", "300")
	code, out, diagnostic = backgroundCLI(t, "wait", "--healthy", unchecked)
	if code != 125 || out != "" || !strings.Contains(diagnostic, "no healthcheck") {
		t.Fatalf("missing check: %d %q %q", code, out, diagnostic)
	}
}

func TestWaitHealthyCancellationLeavesWorkloadRunning(t *testing.T) {
	require(t)
	id := startBackground(t, backgroundName(t), []string{"--health-cmd", "false", "--health-interval", "50ms", "--health-retries", "1"}, "/bin/sleep", "300")
	awaitHealth(t, id, container.HealthUnhealthy)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "wait", "--healthy", "--timeout", "0s", id)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() { cancel(); <-done })
	time.Sleep(200 * time.Millisecond)
	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		done <- err
		if cmd.ProcessState.ExitCode() != 130 || stdout.Len() != 0 || stderr.Len() != 0 {
			t.Fatalf("cancel: %v exit=%d stdout=%q stderr=%q", err, cmd.ProcessState.ExitCode(), stdout.String(), stderr.String())
		}
	case <-ctx.Done():
		t.Fatal("canceled wait did not finish")
	}
	waitBackground(t, id, "running")
}
