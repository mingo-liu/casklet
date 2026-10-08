//go:build linux

package integration

import (
	"context"
	"encoding/json"
	"github.com/mingo-liu/casklet/internal/storage"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiskUsageAndUnusedImagePrune(t *testing.T) {
	require(t)
	first := importImage(t, imageTemplate(t))
	second := importImage(t, imageTemplate(t))
	id := startImageContainer(t, backgroundName(t), first, "/bin/sleep", "300")
	backgroundSuccess(t, "stop", id)
	preview := backgroundSuccess(t, "image", "prune", "--dry-run")
	if strings.Contains(preview, first) || !strings.Contains(preview, second) {
		t.Fatalf("preview %q", preview)
	}
	var report storage.Report
	if err := json.Unmarshal([]byte(backgroundSuccess(t, "system", "df", "--json")), &report); err != nil {
		t.Fatal(err)
	}
	if report.Filesystem.TotalBytes == 0 || len(report.Categories) != 5 {
		t.Fatalf("report %+v", report)
	}
	removed := backgroundSuccess(t, "image", "prune")
	if strings.Contains(removed, first) || !strings.Contains(removed, second) {
		t.Fatalf("removed %q", removed)
	}
	backgroundSuccess(t, "start", id)
	backgroundSuccess(t, "stop", id)
}
func TestDiskScanSkipsSameFilesystemBindMounts(t *testing.T) {
	require(t)
	root := t.TempDir()
	source := t.TempDir()
	os.WriteFile(filepath.Join(source, "external"), make([]byte, 1<<20), 0600)
	target := filepath.Join(root, "mounted")
	os.Mkdir(target, 0755)
	before, err := storage.AllocatedBytes(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Mount(source, target, "", unix.MS_BIND, ""); err != nil {
		t.Fatal(err)
	}
	defer unix.Unmount(target, 0)
	after, err := storage.AllocatedBytes(context.Background(), root)
	if err != nil || after > before {
		t.Fatalf("counted mounted data: %d > %d (%v)", after, before, err)
	}
}
