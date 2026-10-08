//go:build linux

package image

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPruneSkipsLeasesAndReferencesAndRechecksPreview(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	source := testTemplate(t)
	first, err := s.Import(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "marker"), []byte("second"), 0644); err != nil {
		t.Fatal(err)
	}
	second, err := s.Import(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	_, _, lease, err := s.Acquire(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	preview, err := s.Prune(ctx, true, unused)
	if err != nil || len(preview) != 1 || preview[0].ID != second.ID {
		t.Fatalf("preview %+v %v", preview, err)
	}
	if _, err := s.Resolve(ctx, second.ID); err != nil {
		t.Fatal("preview deleted image", err)
	}
	removed, err := s.Prune(ctx, false, func(context.Context, string) (bool, error) { return true, nil })
	if err != nil || len(removed) != 0 {
		t.Fatalf("reference recheck: %+v %v", removed, err)
	}
	removed, err = s.Prune(ctx, false, unused)
	if err != nil || len(removed) != 1 || removed[0].ID != second.ID {
		t.Fatalf("removal %+v %v", removed, err)
	}
	if _, err := s.Resolve(ctx, second.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := s.Resolve(ctx, first.ID); err != nil {
		t.Fatal("removed leased image", err)
	}
}
func TestPrunePreservesImagesWhenReferenceChecksFail(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	r, err := s.Import(ctx, testTemplate(t))
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("reference check unavailable")
	if _, err := s.Prune(ctx, false, func(context.Context, string) (bool, error) { return false, failure }); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if _, err := s.Resolve(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
}
