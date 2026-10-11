//go:build darwin

package macos

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/mingo-liu/casklet/internal/image"
	"github.com/mingo-liu/casklet/internal/registryauth"
)

func registryInputCommand(t *testing.T, input string, args ...string) (string, string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, client(t), args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = strings.NewReader(input), &stdout, &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("registry command timed out: %s", stderr.String())
	}
	code := 0
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatal(err)
		}
		code = exit.ExitCode()
	}
	return stdout.String(), stderr.String(), code
}

func TestPrivateRegistryLoginPullAutomaticRunAndLogout(t *testing.T) {
	client(t)
	fixture := progressRegistryDetails(t, true)
	registry, err := registryauth.Registry(fixture.References[0])
	if err != nil {
		t.Fatal(err)
	}
	store, err := registryauth.OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(registry); !errors.Is(err, registryauth.ErrNotFound) {
		t.Fatalf("temporary registry already has credentials: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Delete(registry); err != nil {
			t.Error(err)
		}
	})
	out, diagnostic, code := command(t, "image", "pull", fixture.References[0])
	if code == 0 || out != "" {
		t.Fatalf("anonymous access unexpectedly succeeded: %d %q %q", code, out, diagnostic)
	}
	out, diagnostic, code = registryInputCommand(t, "wrong-token\n", "login", "-u", "casklet-test", "--password-stdin", registry)
	if code != 0 || !strings.Contains(out, "access is checked on pull") {
		t.Fatalf("login save: %d %q %q", code, out, diagnostic)
	}
	out, diagnostic, code = command(t, "image", "pull", fixture.References[0])
	if code == 0 || out != "" || !strings.Contains(diagnostic, "authenticated registry pull failed") || strings.Contains(diagnostic, "wrong-token") || strings.Contains(diagnostic, "casklet-fixture-token") {
		t.Fatalf("failed authentication leaked credentials: %d %q %q", code, out, diagnostic)
	}
	out, diagnostic, code = registryInputCommand(t, "casklet-fixture-token\n", "login", "--username", "casklet-test", "--password-stdin", registry)
	if code != 0 {
		t.Fatalf("save credentials: %d %q %q", code, out, diagnostic)
	}
	out, diagnostic, code = command(t, "image", "pull", fixture.References[0])
	if code != 0 || image.ValidateID(strings.TrimSpace(out)) != nil || strings.Count(out, "\n") != 1 {
		t.Fatalf("authenticated pull: %d %q %q", code, out, diagnostic)
	}
	id := strings.TrimSpace(out)
	t.Cleanup(func() { command(t, "image", "rm", id) })
	// The second reference is uncached, exercising credential preparation before
	// normal SSH workload input. Only the workload receives the original stdin.
	out, diagnostic, code = registryInputCommand(t, "workload-input-exact\n", "run", "--image", fixture.References[1], "--", "/bin/busybox", "cat")
	if code != 0 || out != "workload-input-exact\n" || strings.Contains(diagnostic, "casklet-fixture-token") {
		t.Fatalf("authenticated automatic run/stdin: %d %q %q", code, out, diagnostic)
	}
	success(t, "logout", registry)
	if _, err := store.Get(registry); !errors.Is(err, registryauth.ErrNotFound) {
		t.Fatalf("logout left credential: %v", err)
	}
	out, diagnostic, code = command(t, "run", "-t", "--image", fixture.References[1])
	// A PTY merges the guest's cached-image diagnostic with workload output.
	if code != 0 || out != "Using cached image "+id+"\nimage-progress-ok\r\n" || diagnostic != "" {
		t.Fatalf("cached terminal run after logout: %d %q %q", code, out, diagnostic)
	}
	out, _, code = command(t, "image", "pull", fixture.References[1])
	if code == 0 || out != "" {
		t.Fatal("logout retained authentication")
	}
	out, diagnostic, code = command(t, "run", "--image", fixture.References[0])
	if code != 0 || out != "image-progress-ok\n" {
		t.Fatalf("failed refresh changed cached image: %d %q %q", code, out, diagnostic)
	}
}
