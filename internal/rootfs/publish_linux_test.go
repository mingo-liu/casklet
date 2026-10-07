//go:build linux

package rootfs

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPublishRejectsStagedSymlinkWithoutReplacingDestination(t *testing.T) {
	outside := template(t)
	staged := filepath.Join(t.TempDir(), "staged")
	if err := os.Symlink(outside, staged); err != nil {
		t.Fatal(err)
	}
	destination := template(t)
	before, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if err := Publish(context.Background(), staged, destination); err == nil {
		t.Fatal("published a symlink as a managed template")
	}
	after, err := os.Stat(destination)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("refused publication replaced destination: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "bin/busybox")); err != nil {
		t.Fatalf("refused publication changed outside source: %v", err)
	}
}
