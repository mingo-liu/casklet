//go:build linux

package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mingo-liu/casklet/internal/config"
	"github.com/mingo-liu/casklet/internal/rootfs"
)

func TestPrepareRetainedRootPublishesIndependentCopy(t *testing.T) {
	source, parent := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "data"), []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	retained := filepath.Join(parent, "rootfs")
	path, err := prepareRunRootFS(context.Background(), source, "", retained, false)
	if err != nil || path != retained {
		t.Fatalf("prepare retained root: %q, %v", path, err)
	}
	if err := os.WriteFile(filepath.Join(retained, "data"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(source, "data"))
	if err != nil || string(data) != "original" {
		t.Fatalf("template changed: %q, %v", data, err)
	}
	// Restart keeps writes even when the original template no longer exists.
	if err := os.RemoveAll(source); err != nil {
		t.Fatal(err)
	}
	path, err = prepareRunRootFS(context.Background(), source, "", retained, true)
	if err != nil || path != retained {
		t.Fatalf("reuse retained root: %q, %v", path, err)
	}
	data, err = os.ReadFile(filepath.Join(path, "data"))
	if err != nil || string(data) != "changed" {
		t.Fatalf("retained writes lost: %q, %v", data, err)
	}
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 1 || entries[0].Name() != "rootfs" {
		t.Fatalf("staging directory leaked: %v, %v", entries, err)
	}
}

func TestPrepareRetainedRootFailureRemovesStaging(t *testing.T) {
	for _, failure := range []string{"canceled", "invalid source"} {
		t.Run(failure, func(t *testing.T) {
			parent, source := t.TempDir(), t.TempDir()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if failure == "canceled" {
				cancel()
			} else {
				source = filepath.Join(source, "missing")
			}
			path, err := prepareRunRootFS(ctx, source, "", filepath.Join(parent, "rootfs"), false)
			if path != "" || err == nil {
				t.Fatalf("invalid preparation succeeded: %q, %v", path, err)
			}
			if failure == "canceled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("lost cancellation: %v", err)
			}
			entries, err := os.ReadDir(parent)
			if err != nil || len(entries) != 0 {
				t.Fatalf("partial root or staging directory leaked: %v, %v", entries, err)
			}
		})
	}
}

func TestAcquireRunTemplateRejectsRetainedSymlink(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rootfs")
	if err := os.Symlink(t.TempDir(), path); err != nil {
		t.Fatal(err)
	}
	source, ready, err := acquireRunTemplate(context.Background(), config.Config{}, path)
	if err == nil || source != nil || ready {
		t.Fatalf("retained symlink accepted: %v, %v, %v", source, ready, err)
	}
}

func TestPrepareImageOverlayPublishesSparseStorageAndKeepsWrites(t *testing.T) {
	lower, parent, run := t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(lower, "unchanged"), make([]byte, 1<<20), 0600); err != nil {
		t.Fatal(err)
	}
	id := "sha256:" + strings.Repeat("a", 64)
	retained := filepath.Join(parent, "rootfs")
	overlay, err := prepareImageOverlay(context.Background(), lower, id, run, retained, false)
	if err != nil {
		t.Fatal(err)
	}
	if overlay.Storage != rootfs.OverlayStoragePath(retained) || overlay.Target != filepath.Join(run, "rootfs") || overlay.Lower != lower {
		t.Fatalf("wrong overlay layout: %+v", overlay)
	}
	if _, err := os.Lstat(retained); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy root path exists: %v", err)
	}
	upper := filepath.Join(overlay.Storage, "upper")
	if _, err := os.Lstat(filepath.Join(upper, "unchanged")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lower data copied: %v", err)
	}
	if err := os.WriteFile(filepath.Join(upper, "written"), []byte("kept"), 0600); err != nil {
		t.Fatal(err)
	}
	restarted, err := prepareImageOverlay(context.Background(), lower, id, t.TempDir(), retained, true)
	if err != nil || restarted.Storage != overlay.Storage {
		t.Fatalf("restart overlay: %+v, %v", restarted, err)
	}
	data, err := os.ReadFile(filepath.Join(upper, "written"))
	if err != nil || string(data) != "kept" {
		t.Fatalf("overlay writes lost: %q, %v", data, err)
	}
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 1 || entries[0].Name() != "rootfs.overlay" {
		t.Fatalf("overlay staging leaked: %v, %v", entries, err)
	}
}

func TestAcquireLegacyRootIgnoresImageAndBackendMarkerFiles(t *testing.T) {
	retained := filepath.Join(t.TempDir(), "rootfs")
	if err := os.Mkdir(retained, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(retained, ".casklet-overlay-v1.json"), []byte("ordinary workload data"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(rootfs.OverlayStoragePath(retained), 0700); err != nil {
		t.Fatal(err)
	}
	source, ready, err := acquireRunTemplate(context.Background(), config.Config{RootFS: "/missing", Image: "missing-image"}, retained)
	if err != nil || !ready || source.Path != retained {
		t.Fatalf("legacy root requires missing source: %+v, %v, %v", source, ready, err)
	}
	source.Close()
}

func TestAcquireRetainedOverlayRejectsDamageWithoutReinitializing(t *testing.T) {
	for _, kind := range []string{"missing marker", "wrong image", "rootless", "userns", "storage symlink"} {
		t.Run(kind, func(t *testing.T) {
			retained := filepath.Join(t.TempDir(), "rootfs")
			storage := rootfs.OverlayStoragePath(retained)
			if err := os.Mkdir(storage, 0700); err != nil {
				t.Fatal(err)
			}
			id := "sha256:" + strings.Repeat("a", 64)
			if err := rootfs.CreateOverlayStorage(context.Background(), t.TempDir(), storage, id); err != nil {
				t.Fatal(err)
			}
			cfg := config.Config{Image: id}
			switch kind {
			case "missing marker":
				if err := os.Remove(filepath.Join(storage, ".casklet-overlay-v1.json")); err != nil {
					t.Fatal(err)
				}
			case "wrong image":
				cfg.Image = "sha256:" + strings.Repeat("b", 64)
			case "rootless":
				cfg.Rootless = true
			case "userns":
				cfg.UserNS = true
			case "storage symlink":
				other := storage + ".saved"
				if err := os.Rename(storage, other); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(other, storage); err != nil {
					t.Fatal(err)
				}
			}
			source, ready, err := acquireRunTemplate(context.Background(), cfg, retained)
			if err == nil || ready || source != nil {
				t.Fatalf("damaged overlay accepted: %+v, %v, %v", source, ready, err)
			}
			if _, err := os.Lstat(retained); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("damaged overlay was reinitialized: %v", err)
			}
		})
	}
}
