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

func TestOverlayStorageRejectsUnsafeRecordsAndDirectories(t *testing.T) {
	for _, kind := range []string{"marker symlink", "marker hardlink", "marker public", "marker suffix", "invalid identity", "unknown version", "unknown field", "public storage", "upper symlink", "work symlink", "public work", "storage symlink"} {
		t.Run(kind, func(t *testing.T) {
			lower, storage := t.TempDir(), t.TempDir()
			if err := os.Chmod(storage, 0700); err != nil {
				t.Fatal(err)
			}
			id := "sha256:" + strings.Repeat("b", 64)
			if err := CreateOverlayStorage(context.Background(), lower, storage, id); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(storage, overlayMarker)
			var err error
			switch kind {
			case "marker symlink", "marker hardlink":
				outside := filepath.Join(t.TempDir(), "outside")
				if err := os.WriteFile(outside, []byte(`{"version":1,"image":"`+id+`"}`), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(marker); err != nil {
					t.Fatal(err)
				}
				if kind == "marker symlink" {
					err = os.Symlink(outside, marker)
				} else {
					err = os.Link(outside, marker)
				}
			case "marker public":
				err = os.Chmod(marker, 0644)
			case "marker suffix":
				err = os.WriteFile(marker, []byte(`{"version":1,"image":"`+id+`"} {}`), 0600)
			case "invalid identity":
				err = os.WriteFile(marker, []byte(`{"version":1,"image":"../../outside"}`), 0600)
			case "unknown version":
				err = os.WriteFile(marker, []byte(`{"version":2,"image":"`+id+`"}`), 0600)
			case "unknown field":
				err = os.WriteFile(marker, []byte(`{"version":1,"image":"`+id+`","lower":"/outside"}`), 0600)
			case "public storage":
				err = os.Chmod(storage, 0755)
			case "public work":
				err = os.Chmod(filepath.Join(storage, "work"), 0755)
			case "upper symlink", "work symlink":
				name := "upper"
				if kind == "work symlink" {
					name = "work"
				}
				path := filepath.Join(storage, name)
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				err = os.Symlink(t.TempDir(), path)
			case "storage symlink":
				link := filepath.Join(t.TempDir(), "link")
				err = os.Symlink(storage, link)
				storage = link
			}
			if err != nil {
				t.Fatal(err)
			}
			if id, ready, err := ReadOverlayImageID(storage); err == nil || ready || id != "" {
				t.Fatalf("unsafe storage accepted: %q, %v, %v", id, ready, err)
			}
		})
	}
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
