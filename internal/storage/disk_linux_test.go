//go:build linux

package storage

import (
	"context"
	"errors"
	"golang.org/x/sys/unix"
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
	if len(r.Categories) != 6 || r.Filesystem.TotalBytes == 0 || r.Filesystem.AvailableBytes > r.Filesystem.FreeBytes {
		t.Fatalf("report %+v", r)
	}
	if r.Categories[3].AllocatedBytes >= 1<<20 {
		t.Fatalf("counted sparse logical size: %+v", r.Categories[3])
	}
}

func TestImageCategoriesPartitionCacheWithoutDoubleCounting(t *testing.T) {
	root := t.TempDir()
	images := filepath.Join(root, "images")
	cache := filepath.Join(images, ".blobs")
	if err := os.MkdirAll(cache, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, "blob"), make([]byte, 8192), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(images, "unpacked"), make([]byte, 16384), 0600); err != nil {
		t.Fatal(err)
	}
	total, err := AllocatedBytes(context.Background(), images)
	if err != nil {
		t.Fatal(err)
	}
	report, err := usageAt(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if report.Categories[0].Kind != "images" || report.Categories[1].Kind != "image-cache" || report.Categories[1].AllocatedBytes < 8192 || report.Categories[0].AllocatedBytes < 16384 {
		t.Fatal(report)
	}
	if report.Categories[0].AllocatedBytes+report.Categories[1].AllocatedBytes != total {
		t.Fatalf("partition differs from allocation: %+v total=%d", report, total)
	}
}

func TestImagePartitionExcludesMountedCache(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("cache mount regression requires root")
	}
	images := t.TempDir()
	cache := filepath.Join(images, ".blobs")
	if err := os.Mkdir(cache, 0700); err != nil {
		t.Fatal(err)
	}
	before, _, err := imageAllocation(context.Background(), images)
	if err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "external"), make([]byte, 1<<20), 0600); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mount(source, cache, "", unix.MS_BIND, ""); err != nil {
		if errors.Is(err, unix.EPERM) {
			t.Skip("cache mount regression requires mount capabilities")
		}
		t.Fatal(err)
	}
	defer unix.Unmount(cache, 0)
	after, blobs, err := imageAllocation(context.Background(), images)
	if err != nil || after != before || blobs != 0 {
		t.Fatalf("mounted cache counted: before=%d after=%d blobs=%d %v", before, after, blobs, err)
	}
}
