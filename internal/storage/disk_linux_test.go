//go:build linux

package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAllocationExcludesSymlinksAndDeduplicatesHardlinks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file")
	os.WriteFile(path, make([]byte, 8192), 0600)
	before, err := AllocatedBytes(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	os.Link(path, filepath.Join(dir, "alias"))
	after, err := AllocatedBytes(context.Background(), dir)
	if err != nil || after != before {
		t.Fatalf("hardlink counted twice: %d %d %v", before, after, err)
	}
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "large"), make([]byte, 1<<20), 0600)
	os.Symlink(outside, filepath.Join(dir, "outside"))
	after, err = AllocatedBytes(context.Background(), dir)
	if err != nil || after > before+4096 {
		t.Fatalf("followed symlink: %d %d %v", before, after, err)
	}
	if _, err := AllocatedBytes(context.Background(), filepath.Join(dir, "outside")); err == nil {
		t.Fatal("accepted root symlink")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := AllocatedBytes(ctx, dir); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
func TestDiskUsageCountsCategoriesAndSparseAllocation(t *testing.T) {
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, "volumes"), 0700)
	f, err := os.Create(filepath.Join(root, "volumes", "sparse"))
	if err != nil {
		t.Fatal(err)
	}
	f.Truncate(1 << 30)
	f.Close()
	r, err := usageAt(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Categories) != 5 || r.Filesystem.TotalBytes == 0 || r.Filesystem.AvailableBytes > r.Filesystem.FreeBytes {
		t.Fatalf("report %+v", r)
	}
	if r.Categories[2].AllocatedBytes >= 1<<20 {
		t.Fatalf("counted sparse logical size: %+v", r.Categories[2])
	}
}
