//go:build linux

package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSecretSnapshotsRestartAndCleanup(t *testing.T) {
	require(t)
	source := filepath.Join(t.TempDir(), "token")
	os.WriteFile(source, []byte("first-private-value"), 0600)
	option := "source=" + source + ",target=token"
	for _, user := range []string{"0", "1000:1000"} {
		success(t, []string{"--secret", option, "--user", user, "--read-only"}, "/bin/sh", "-c", `set -eu; test "$(cat /run/secrets/token)" = first-private-value; if echo changed > /run/secrets/token; then exit 1; fi; if rm /run/secrets/token; then exit 1; fi; if touch /run/secrets/new; then exit 1; fi`)
	}
	id := strings.TrimSpace(backgroundSuccess(t, "run", "-d", "--rootfs", template, "--secret", option, "--", "/bin/sleep", "300"))
	t.Cleanup(func() { backgroundCLI(t, "stop", id); backgroundCLI(t, "rm", id) })
	os.WriteFile(source, []byte("second-private-value"), 0600)
	if got := backgroundSuccess(t, "exec", id, "--", "/bin/cat", "/run/secrets/token"); got != "first-private-value" {
		t.Fatalf("running snapshot: %q", got)
	}
	if got := backgroundSuccess(t, "inspect", id); strings.Contains(got, "private-value") {
		t.Fatal("inspect disclosed content")
	}
	backgroundSuccess(t, "restart", id)
	if got := backgroundSuccess(t, "exec", id, "--", "/bin/cat", "/run/secrets/token"); got != "second-private-value" {
		t.Fatalf("restart snapshot: %q", got)
	}
	backgroundSuccess(t, "stop", id)
	if _, err := os.Stat(filepath.Join("/var/lib/casklet/containers", id, "rootfs/run/secrets/token")); !os.IsNotExist(err) {
		t.Fatalf("secret persisted: %v", err)
	}
	os.Remove(source)
	if code, _, _ := backgroundCLI(t, "start", id); code == 0 {
		t.Fatal("missing source accepted")
	}
	backgroundSuccess(t, "rm", id)
}
