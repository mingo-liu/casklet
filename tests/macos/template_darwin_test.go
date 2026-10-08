//go:build darwin

package macos

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func guestRootCommand(t *testing.T, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "limactl", append([]string{"shell", "casklet-runtime", "sudo", "-n", "--"}, args...)...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("guest command: %w: %s", err, output)
	}
	return string(output), nil
}

func TestMachineRepairsBuiltinTemplate(t *testing.T) {
	success(t, "doctor")
	const template = "/var/lib/casklet/templates/busybox"
	backup, err := guestRootCommand(t, "mktemp", "-d", "/var/lib/casklet/templates/.macos-test-backup-XXXXXXXX")
	if err != nil {
		t.Fatal(err)
	}
	backup = strings.TrimSpace(backup)
	if !strings.HasPrefix(backup, "/var/lib/casklet/templates/.macos-test-backup-") {
		t.Fatalf("unexpected backup directory: %q", backup)
	}
	if _, err := guestRootCommand(t, "cp", "-a", "--", template, backup+"/rootfs"); err != nil {
		_, _ = guestRootCommand(t, "rmdir", backup)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Restore the original even when a regression prevents automatic repair.
		// This test is sequential and never touches retained container roots.
		script := `set -eu; rm -rf -- "$1"; mv -T -- "$2/rootfs" "$1"; rmdir -- "$2"`
		if _, err := guestRootCommand(t, "/bin/sh", "-c", script, "restore-template", template, backup); err != nil {
			t.Error(err)
		}
	})
	// A running workload already owns its private root and must survive repair.
	id := detached(t, "--", "/bin/sleep", "300")
	for _, failure := range []string{"missing busybox", "nonexecutable busybox", "corrupt busybox", "missing directory", "missing applet", "invalid metadata"} {
		t.Run(failure, func(t *testing.T) {
			var args []string
			switch failure {
			case "missing busybox":
				args = []string{"rm", "--", template + "/bin/busybox"}
			case "nonexecutable busybox":
				args = []string{"chmod", "0644", template + "/bin/busybox"}
			case "corrupt busybox":
				args = []string{"/bin/sh", "-c", `printf 'invalid ELF' > "$1"`, "corrupt-template", template + "/bin/busybox"}
			case "missing directory":
				args = []string{"rmdir", "--", template + "/proc"}
			case "missing applet":
				args = []string{"rm", "--", template + "/bin/cat"}
			case "invalid metadata":
				args = []string{"/bin/sh", "-c", `printf '{}' > "$1"`, "corrupt-template", template + "/.casklet-rootfs.json"}
			}
			if _, err := guestRootCommand(t, args...); err != nil {
				t.Fatal(err)
			}
			if out := success(t, "run", "--", "/bin/sh", "-c", "test -d /proc; printf 'template-repaired'"); out != "template-repaired" {
				t.Fatalf("repaired workload output: %q", out)
			}
			if out := success(t, "exec", id, "--", "/bin/echo", "retained-root-preserved"); out != "retained-root-preserved\n" {
				t.Fatalf("repair changed an active container: %q", out)
			}
		})
	}
}
