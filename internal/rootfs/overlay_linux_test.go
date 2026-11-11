//go:build linux

package rootfs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOverlayStorageSharesLowerAndPreservesRootMetadata(t *testing.T) {
	lower, storage := t.TempDir(), t.TempDir()
	if err := os.Chmod(storage, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(lower, 0751); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lower, "large-data"), make([]byte, 1<<20), 0644); err != nil {
		t.Fatal(err)
	}
	id := "sha256:" + strings.Repeat("a", 64)
	if err := CreateOverlayStorage(context.Background(), lower, storage, id); err != nil {
		t.Fatal(err)
	}
	actual, ready, err := ReadOverlayImageID(storage)
	if err != nil || !ready || actual != id {
		t.Fatalf("overlay identity = %q, %v, %v", actual, ready, err)
	}
	upper, err := os.Stat(filepath.Join(storage, "upper"))
	if err != nil || upper.Mode().Perm() != 0751 {
		t.Fatalf("upper root permissions = %v, %v", upper, err)
	}
	entries, err := os.ReadDir(filepath.Join(storage, "upper"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("image data was copied into upper: %v, %v", entries, err)
	}
}

func TestOverlayStorageRejectsMarkerSymlink(t *testing.T) {
	storage, id := overlayStorage(t)
	outside := filepath.Join(t.TempDir(), "outside")
	must(t, os.WriteFile(outside, []byte(`{"version":1,"image":"`+id+`"}`), 0600))
	marker := filepath.Join(storage, _overlayMarker)
	must(t, os.Remove(marker))
	must(t, os.Symlink(outside, marker))
	assertOverlayRejected(t, storage)
}

func TestOverlayStorageRejectsMarkerHardlink(t *testing.T) {
	storage, id := overlayStorage(t)
	outside := filepath.Join(t.TempDir(), "outside")
	must(t, os.WriteFile(outside, []byte(`{"version":1,"image":"`+id+`"}`), 0600))
	marker := filepath.Join(storage, _overlayMarker)
	must(t, os.Remove(marker))
	must(t, os.Link(outside, marker))
	assertOverlayRejected(t, storage)
}

func TestOverlayStorageRejectsPublicMarker(t *testing.T) {
	storage, _ := overlayStorage(t)
	must(t, os.Chmod(filepath.Join(storage, _overlayMarker), 0644))
	assertOverlayRejected(t, storage)
}

func TestOverlayStorageRejectsInvalidRecords(t *testing.T) {
	id := "sha256:" + strings.Repeat("b", 64)
	tests := []struct {
		name       string
		giveRecord string
	}{
		{name: "suffix", giveRecord: `{"version":1,"image":"` + id + `"} {}`},
		{name: "identity", giveRecord: `{"version":1,"image":"../../outside"}`},
		{name: "version", giveRecord: `{"version":2,"image":"` + id + `"}`},
		{name: "unknown field", giveRecord: `{"version":1,"image":"` + id + `","lower":"/outside"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			storage, _ := overlayStorage(t)
			must(t, os.WriteFile(filepath.Join(storage, _overlayMarker), []byte(tt.giveRecord), 0600))
			assertOverlayRejected(t, storage)
		})
	}
}

func TestOverlayStorageRejectsPublicDirectories(t *testing.T) {
	tests := []string{".", "work"}
	for _, tt := range tests {
		t.Run(tt, func(t *testing.T) {
			storage, _ := overlayStorage(t)
			must(t, os.Chmod(filepath.Join(storage, tt), 0755))
			assertOverlayRejected(t, storage)
		})
	}
}

func TestOverlayStorageRejectsLinkedDirectories(t *testing.T) {
	tests := []string{"upper", "work"}
	for _, tt := range tests {
		t.Run(tt, func(t *testing.T) {
			storage, _ := overlayStorage(t)
			name := filepath.Join(storage, tt)
			must(t, os.Remove(name))
			must(t, os.Symlink(t.TempDir(), name))
			assertOverlayRejected(t, storage)
		})
	}
}

func TestOverlayStorageRejectsLinkedStorage(t *testing.T) {
	storage, _ := overlayStorage(t)
	link := filepath.Join(t.TempDir(), "link")
	must(t, os.Symlink(storage, link))
	assertOverlayRejected(t, link)
}

func TestOverlayStorageKeepsLegacyRootsAndHonorsCancellation(t *testing.T) {
	legacy := t.TempDir()
	if id, ready, err := ReadOverlayImageID(legacy); id != "" || ready || err != nil {
		t.Fatalf("legacy root recognized as overlay: %q, %v, %v", id, ready, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := CreateOverlayStorage(ctx, t.TempDir(), legacy, "sha256:"+strings.Repeat("a", 64)); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled preparation: %v", err)
	}
	entries, err := os.ReadDir(legacy)
	if err != nil || len(entries) != 0 {
		t.Fatalf("canceled preparation wrote storage: %v, %v", entries, err)
	}
}

func overlayStorage(t *testing.T) (string, string) {
	t.Helper()
	lower, storage := t.TempDir(), t.TempDir()
	must(t, os.Chmod(storage, 0700))
	id := "sha256:" + strings.Repeat("b", 64)
	must(t, CreateOverlayStorage(context.Background(), lower, storage, id))
	return storage, id
}

func assertOverlayRejected(t *testing.T, storage string) {
	t.Helper()
	if id, ready, err := ReadOverlayImageID(storage); err == nil || ready || id != "" {
		t.Fatalf("unsafe storage accepted: %q, %v, %v", id, ready, err)
	}
}
