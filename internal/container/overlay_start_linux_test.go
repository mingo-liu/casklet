//go:build linux

package container

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mingo-liu/casklet/internal/config"
	"github.com/mingo-liu/casklet/internal/rootfs"
)

func overlayStartFixture(t *testing.T) (config.Config, string, string) {
	t.Helper()
	lower := t.TempDir()
	retained := filepath.Join(t.TempDir(), "rootfs")
	storage := retained + ".overlay"
	if err := os.Mkdir(storage, 0700); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Image: "sha256:" + strings.Repeat("a", 64), RootFS: "/unavailable-source"}
	if err := rootfs.CreateOverlayStorage(context.Background(), lower, storage, cfg.Image); err != nil {
		t.Fatal(err)
	}
	return cfg, retained, storage
}

func TestRetainedRootCandidateRequiresMatchingOverlayImage(t *testing.T) {
	for _, kind := range []string{"matching", "different image", "no image", "user namespace", "rootless"} {
		t.Run(kind, func(t *testing.T) {
			cfg, retained, _ := overlayStartFixture(t)
			switch kind {
			case "different image":
				cfg.Image = "sha256:" + strings.Repeat("b", 64)
			case "no image":
				cfg.Image = ""
			case "user namespace":
				cfg.UserNS = true
			case "rootless":
				cfg.Rootless = true
			}
			candidate, needsImage, err := retainedRootCandidate(cfg, retained)
			if kind == "matching" {
				if err != nil || !needsImage || candidate != cfg.RootFS {
					t.Fatalf("matching lower was not selected for acquisition: %q, %v, %v", candidate, needsImage, err)
				}
			} else if err == nil || needsImage || candidate != "" {
				t.Fatalf("invalid overlay selected a startup source: %q, %v, %v", candidate, needsImage, err)
			}
		})
	}
}

func TestRetainedRootCandidateRejectsIncompleteOverlay(t *testing.T) {
	for _, kind := range []string{"missing record", "corrupt record", "record symlink", "storage symlink", "missing upper", "missing work"} {
		t.Run(kind, func(t *testing.T) {
			cfg, retained, storage := overlayStartFixture(t)
			marker := filepath.Join(storage, ".casklet-overlay-v1.json")
			var err error
			switch kind {
			case "missing record":
				err = os.Remove(marker)
			case "corrupt record":
				err = os.WriteFile(marker, []byte("invalid backend metadata"), 0600)
			case "record symlink":
				if err := os.Remove(marker); err != nil {
					t.Fatal(err)
				}
				err = os.Symlink(filepath.Join(t.TempDir(), "missing"), marker)
			case "storage symlink":
				moved := filepath.Join(t.TempDir(), "storage")
				if err := os.Rename(storage, moved); err != nil {
					t.Fatal(err)
				}
				err = os.Symlink(moved, storage)
			case "missing upper":
				err = os.Remove(filepath.Join(storage, "upper"))
			case "missing work":
				err = os.Remove(filepath.Join(storage, "work"))
			}
			if err != nil {
				t.Fatal(err)
			}
			if candidate, needsImage, err := retainedRootCandidate(cfg, retained); err == nil || candidate != "" || needsImage {
				t.Fatalf("incomplete overlay selected a startup source: %q, %v, %v", candidate, needsImage, err)
			}
		})
	}
}

func TestRetainedRootCandidateIgnoresLegacyApplicationMarker(t *testing.T) {
	for _, kind := range []string{"ordinary file", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			retained := t.TempDir()
			marker := filepath.Join(retained, ".casklet-overlay-v1.json")
			var err error
			if kind == "ordinary file" {
				err = os.WriteFile(marker, []byte("application data, not backend metadata"), 0644)
			} else {
				err = os.Symlink("/missing-application-data", marker)
			}
			if err != nil {
				t.Fatal(err)
			}
			cfg := config.Config{Image: "sha256:" + strings.Repeat("a", 64), RootFS: "/unavailable-source"}
			candidate, needsImage, err := retainedRootCandidate(cfg, retained)
			if err != nil || needsImage || candidate != retained {
				t.Fatalf("legacy application file affected backend selection: %q, %v, %v", candidate, needsImage, err)
			}
		})
	}
}

func TestRetainedRootCandidateKeepsUnpreparedSources(t *testing.T) {
	retained := filepath.Join(t.TempDir(), "rootfs")
	for _, image := range []string{"", "sha256:" + strings.Repeat("a", 64)} {
		cfg := config.Config{Image: image, RootFS: "/original-source"}
		candidate, needsImage, err := retainedRootCandidate(cfg, retained)
		if err != nil || candidate != cfg.RootFS || needsImage != (image != "") {
			t.Fatalf("initial source selection: %q, %v, %v", candidate, needsImage, err)
		}
	}
}
