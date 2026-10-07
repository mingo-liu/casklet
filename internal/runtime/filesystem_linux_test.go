//go:build linux

package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/mingo-liu/mini-docker/internal/config"
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
