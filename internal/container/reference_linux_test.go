//go:build linux

package container

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func recordWithID(t *testing.T, store *Store, id, name string) Record {
	t.Helper()
	record := createTestRecord(t, store, name)
	path := filepath.Join(store.root, record.ID)
	record.ID = id
	if err := store.writeJSON(path, "state.json", record, maxRecordBytes); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, filepath.Join(store.root, id)); err != nil {
		t.Fatal(err)
	}
	return record
}

func TestContainerPrefixAmbiguityAndNamePrecedence(t *testing.T) {
	store, ctx := testStore(t), context.Background()
	prefix := "406e742c72ac"
	first := recordWithID(t, store, prefix+strings.Repeat("a", 20), "first")
	second := recordWithID(t, store, prefix+strings.Repeat("b", 20), "second")
	for _, ref := range []string{prefix, prefix[:1]} {
		if _, err := store.Get(ctx, ref); !errors.Is(err, ErrAmbiguousID) {
			t.Fatalf("ambiguous lookup %s: %v", ref, err)
		}
		if err := store.Remove(ctx, ref); !errors.Is(err, ErrAmbiguousID) {
			t.Fatalf("ambiguous removal %s: %v", ref, err)
		}
	}
	for _, record := range []Record{first, second} {
		for _, ref := range []string{record.ID, record.ID[:13], record.Name} {
			if got, err := store.Get(ctx, ref); err != nil || got.ID != record.ID {
				t.Fatalf("lookup %s: %+v %v", ref, got, err)
			}
		}
	}
	named := recordWithID(t, store, strings.Repeat("c", 32), prefix)
	if got, err := store.Get(ctx, prefix); err != nil || got.ID != named.ID {
		t.Fatalf("exact name did not win over ambiguous prefix: %+v %v", got, err)
	}
	if err := store.Update(ctx, first.ID, func(r *Record) error { r.State = StateExited; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := store.Remove(ctx, first.ID[:13]); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, first.ID[:13]); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removed prefix resolved: %v", err)
	}
	if got, err := store.Get(ctx, second.ID); err != nil || got.ID != second.ID {
		t.Fatalf("removal changed another container: %+v %v", got, err)
	}
}
