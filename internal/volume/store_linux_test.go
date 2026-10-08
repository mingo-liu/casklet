//go:build linux

package volume

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	return &Store{root: root, owner: uint32(os.Geteuid())}
}
func TestVolumeDataSurvivesReuseAndRemovalIsFenced(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	r, err := s.Create(ctx, "data")
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(s.root, "data", "data", "message")
	if err := os.WriteFile(p, []byte("persistent"), 0600); err != nil {
		t.Fatal(err)
	}
	again, err := s.Create(ctx, "data")
	if err != nil || again != r {
		t.Fatalf("reuse: %+v %v", again, err)
	}
	f, err := s.Acquire(ctx, "data")
	if err != nil {
		t.Fatal(err)
	}
	unused := func(context.Context, string) (bool, error) { return false, nil }
	if err := s.Remove(ctx, "data", unused); !errors.Is(err, ErrInUse) {
		t.Fatalf("active deletion: %v", err)
	}
	f.Close()
	if err := s.Remove(ctx, "data", func(context.Context, string) (bool, error) { return true, nil }); !errors.Is(err, ErrInUse) {
		t.Fatalf("referenced deletion: %v", err)
	}
	if data, err := os.ReadFile(p); err != nil || string(data) != "persistent" {
		t.Fatalf("data: %q %v", data, err)
	}
	if err := s.Remove(ctx, "data", unused); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Inspect(ctx, "data"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}
func TestVolumeRejectsUnsafeArtifactsAndRecoversTransactions(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	outside := t.TempDir()
	marker := filepath.Join(outside, "keep")
	os.WriteFile(marker, []byte("safe"), 0600)
	if err := os.Symlink(outside, filepath.Join(s.root, "linked")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, "linked"); err == nil {
		t.Fatal("accepted symlink")
	}
	os.Remove(filepath.Join(s.root, "linked"))
	s.Create(ctx, "data")
	os.Remove(filepath.Join(s.root, "data", "data"))
	os.Symlink(outside, filepath.Join(s.root, "data", "data"))
	if _, err := s.Acquire(ctx, "data"); err == nil {
		t.Fatal("accepted linked data")
	}
	if err := s.Remove(ctx, "data", func(context.Context, string) (bool, error) { return false, nil }); err == nil {
		t.Fatal("removed unsafe volume")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(s.root, "data", "data"))
	os.Mkdir(filepath.Join(s.root, "data", "data"), 0755)
	stage := filepath.Join(s.root, ".create-abandoned")
	os.Mkdir(stage, 0700)
	os.WriteFile(filepath.Join(stage, "partial"), nil, 0600)
	records, err := s.List(ctx)
	if err != nil || len(records) != 1 {
		t.Fatalf("list: %+v %v", records, err)
	}
	if _, err := os.Stat(stage); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("staging survived")
	}
	f, err := s.lock(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cancelCtx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if _, err := s.List(cancelCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("uncancelable lock: %v", err)
	}
}
