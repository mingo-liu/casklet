//go:build linux

package integration

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestContainerTransactionRecoveryPreservesMountedTrees(t *testing.T) {
	// Establish the production store before creating a private interrupted
	// transaction. Every CLI must preserve it until its mount is removed.
	backgroundSuccess(t, "ps", "--all", "--json")
	path := filepath.Join("/var/lib/mini-docker/containers", fmt.Sprintf(".remove-%032x", time.Now().UnixNano()))
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	mounted := false
	t.Cleanup(func() {
		if mounted {
			if err := unix.Unmount(path, 0); err != nil {
				t.Error(err)
				return
			}
		}
		if err := os.RemoveAll(path); err != nil {
			t.Error(err)
		}
	})
	source := t.TempDir()
	if err := os.Chmod(source, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(source, "keep")
	if err := os.WriteFile(marker, []byte("mounted data"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mount(source, path, "", unix.MS_BIND, ""); err != nil {
		t.Fatal(err)
	}
	mounted = true
	code, _, diagnostic := backgroundCLI(t, "ps", "--all", "--json")
	if code != 125 || !strings.Contains(diagnostic, "mounted subtree") {
		t.Fatalf("mounted transaction accepted: %d %s", code, diagnostic)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "mounted data" {
		t.Fatal("recovery modified mounted data", err)
	}
	if err := unix.Unmount(path, 0); err != nil {
		t.Fatal(err)
	}
	mounted = false
	backgroundSuccess(t, "ps", "--all", "--json")
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unmounted transaction was not recovered", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("recovery deleted source data", err)
	}
}
