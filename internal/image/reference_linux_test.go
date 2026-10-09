//go:build linux

package image

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestImagePrefixesKeepLeasesAndCanonicalReferenceChecks(t *testing.T) {
	store, ctx := testStore(t), context.Background()
	record, err := store.Import(ctx, testTemplate(t))
	if err != nil {
		t.Fatal(err)
	}
	hex := strings.TrimPrefix(record.ID, "sha256:")
	for _, ref := range []string{record.ID, hex, hex[:12], hex[:1], "sha256:" + hex[:12]} {
		got, err := store.Resolve(ctx, ref)
		if err != nil || got.ID != record.ID {
			t.Fatalf("resolve %s: %+v %v", ref, got, err)
		}
		got, tree, lease, err := store.Acquire(ctx, ref)
		if err != nil || got.ID != record.ID || tree != filepath.Join(store.path(record.ID), "rootfs") {
			t.Fatalf("acquire %s: %+v %s %v", ref, got, tree, err)
		}
		if err := store.Remove(ctx, hex[:12], unused); !errors.Is(err, ErrInUse) {
			lease.Close()
			t.Fatalf("short ID bypassed live lease: %v", err)
		}
		lease.Close()
	}
	checks := 0
	referenced := func(_ context.Context, id string) (bool, error) {
		checks++
		if id != record.ID {
			t.Fatalf("reference check received prefix %q", id)
		}
		return true, nil
	}
	if err := store.Remove(ctx, hex[:12], referenced); !errors.Is(err, ErrInUse) || checks != 1 {
		t.Fatalf("short ID bypassed container reference: %d %v", checks, err)
	}
	if err := store.Remove(ctx, hex[:12], unused); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Resolve(ctx, hex[:12]); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removed image prefix: %v", err)
	}
}

func metadataImage(t *testing.T, store *Store, id string) Record {
	t.Helper()
	record := Record{ID: id, Architecture: runtime.GOARCH, CreatedAt: time.Now().UTC()}
	if err := os.Mkdir(store.path(id), 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.path(id), "image.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	return record
}

func TestImageAmbiguityRefusesAcquisitionAndDeletion(t *testing.T) {
	store, ctx := testStore(t), context.Background()
	prefix := "406e742c72ac"
	first := metadataImage(t, store, "sha256:"+prefix+strings.Repeat("a", 52))
	second := metadataImage(t, store, "sha256:"+prefix+strings.Repeat("b", 52))
	for _, ref := range []string{prefix, prefix[:1], "sha256:" + prefix} {
		if _, err := store.Resolve(ctx, ref); !errors.Is(err, ErrAmbiguousID) {
			t.Fatalf("ambiguous resolve %s: %v", ref, err)
		}
		if _, _, lease, err := store.Acquire(ctx, ref); !errors.Is(err, ErrAmbiguousID) || lease != nil {
			t.Fatalf("ambiguous acquire %s: %v", ref, err)
		}
		checks := 0
		if err := store.Remove(ctx, ref, func(context.Context, string) (bool, error) { checks++; return false, nil }); !errors.Is(err, ErrAmbiguousID) || checks != 0 {
			t.Fatalf("ambiguous deletion touched image %s: %d %v", ref, checks, err)
		}
	}
	for _, record := range []Record{first, second} {
		for _, ref := range []string{record.ID, strings.TrimPrefix(record.ID, "sha256:")[:13]} {
			if got, err := store.Resolve(ctx, ref); err != nil || got.ID != record.ID {
				t.Fatalf("unambiguous lookup %s: %+v %v", ref, got, err)
			}
		}
	}
	// An exact cached repository name wins over an ID prefix for run --image.
	ref, err := NormalizeReference(prefix)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(cachedReference{Reference: ref, ID: second.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.root, referenceKey(ref)), data, 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Resolve(ctx, prefix); err != nil || got.ID != second.ID {
		t.Fatalf("exact cached name did not win: %+v %v", got, err)
	}
}
