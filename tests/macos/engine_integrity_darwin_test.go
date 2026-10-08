//go:build darwin

package macos

import (
	"strings"
	"testing"
)

func TestMachineRepairsCorruptExecutableEngine(t *testing.T) {
	success(t, "doctor")
	id := detached(t, "--", "/bin/sleep", "300")
	const engine = "/usr/local/bin/mdocker"
	backup, err := guestRootCommand(t, "mktemp", "/usr/local/bin/.mdocker-integrity-test-XXXXXXXX")
	if err != nil {
		t.Fatal(err)
	}
	backup = strings.TrimSpace(backup)
	if !strings.HasPrefix(backup, "/usr/local/bin/.mdocker-integrity-test-") {
		t.Fatalf("unexpected backup path: %q", backup)
	}
	t.Cleanup(func() {
		if _, err := guestRootCommand(t, "/bin/sh", "-c", `set -eu
if [ -s "$1" ]; then
  install -m 0755 "$1" "$1.restore"
  mv -T -- "$1.restore" "$2"
fi
rm -f -- "$1" "$1.corrupt"`, "restore-engine", backup, engine); err != nil {
			t.Error(err)
		}
	})
	if _, err := guestRootCommand(t, "cp", "--", engine, backup); err != nil {
		t.Fatal(err)
	}
	// Leave the matching installation marker intact. Atomic replacement also
	// preserves the executable pinned by the running container's supervisor.
	if _, err := guestRootCommand(t, "/bin/sh", "-c", `set -eu
printf '#!/bin/sh\nexit 42\n' > "$1.corrupt"
chmod 0755 "$1.corrupt"
mv -T -- "$1.corrupt" "$2"`, "corrupt-engine", backup, engine); err != nil {
		t.Fatal(err)
	}
	if out := success(t, "run", "--", "/bin/echo", "integrity-repaired"); out != "integrity-repaired\n" {
		t.Fatalf("repaired engine output: %q", out)
	}
	if out := success(t, "exec", id, "--", "/bin/echo", "supervisor-preserved"); out != "supervisor-preserved\n" {
		t.Fatalf("repair affected the running container: %q", out)
	}
	_, diagnostic, code := command(t, "doctor")
	if code != 0 || diagnostic != "" {
		t.Fatalf("healthy installation was not reused: exit=%d stderr=%q", code, diagnostic)
	}
}
