//go:build linux

package integration

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGenericRootfsWithoutBusyBoxOrMountTargets(t *testing.T) {
	require(t)
	tree := t.TempDir()
	if err := os.Chmod(tree, 0755); err != nil {
		t.Fatal(err)
	}
	program, err := os.ReadFile(os.Getenv("CASKLET_HELPER"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree, "app"), program, 0755); err != nil {
		t.Fatal(err)
	}
	for _, user := range []string{"0:0", "123:456"} {
		code, out, stderr := start(t, "", "run", "--rootfs", tree, "--user", user, "--", "/app", "security").wait(t)
		if code != 0 || out != "security-ok\n" {
			t.Fatalf("generic root user %s: %d %q %q", user, code, out, stderr)
		}
	}
}
