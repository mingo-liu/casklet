//go:build linux

package container

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestLabelsPersistAcrossReopeningAndExecutions(t *testing.T) {
	store := testStore(t)
	cfg := testConfig()
	cfg.Labels = map[string]string{"project": "demo", "role": "api", "empty": ""}
	record, err := store.Create(context.Background(), cfg, "labeled")
	if err != nil {
		t.Fatal(err)
	}
	// Creation must return owned listing metadata rather than aliasing the caller.
	cfg.Labels["project"] = "changed"
	if record.Labels["project"] != "demo" {
		t.Fatal("creation aliased label metadata")
	}
	reopened, err := newStoreAt(store.root)
	if err != nil {
		t.Fatal(err)
	}
	got, stored, err := reopened.Snapshot(context.Background(), record.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"project": "demo", "role": "api", "empty": ""}
	if !reflect.DeepEqual(got.Labels, want) || !reflect.DeepEqual(stored.Labels, want) {
		t.Fatalf("record %v config %v", got.Labels, stored.Labels)
	}
	inspection := inspectRecord(got, stored)
	if !reflect.DeepEqual(inspection.Labels, want) {
		t.Fatalf("inspection %v", inspection.Labels)
	}
	inspection.Labels["project"] = "changed"
	if got.Labels["project"] != "demo" {
		t.Fatal("inspection aliased label metadata")
	}
	finishTestExecution(t, reopened, got, 0)
	lock, err := reopened.AcquireOperation(context.Background(), got.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	next, err := reopened.BeginExecution(context.Background(), got.ID, got.Generation)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(next.Labels, want) {
		t.Fatalf("restart lost labels: %v", next.Labels)
	}
	listed, err := reopened.List(context.Background())
	if err != nil || len(listed) != 1 || !reflect.DeepEqual(listed[0].Labels, want) {
		t.Fatalf("list %v %v", listed, err)
	}
}

func TestLabelMutationsCannotSplitListingAndConfiguration(t *testing.T) {
	store := testStore(t)
	cfg := testConfig()
	cfg.Labels = map[string]string{"project": "original"}
	record, err := store.Create(context.Background(), cfg, "immutable-labels")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(context.Background(), record.ID, func(r *Record) error { r.Labels["project"] = "changed"; return nil }); err == nil {
		t.Fatal("Update changed immutable labels")
	}
	if err := store.Complete(context.Background(), record.ID, record.Generation, func(r *Record) { r.Labels = map[string]string{"other": "changed"}; r.State = StateExited }); err == nil {
		t.Fatal("Complete changed immutable labels")
	}
	got, stored, err := store.Snapshot(context.Background(), record.ID)
	if err != nil || got.State != StateCreated || got.Labels["project"] != "original" || !reflect.DeepEqual(got.Labels, stored.Labels) {
		t.Fatalf("snapshot %+v config %v err %v", got, stored.Labels, err)
	}
}

func TestEscapedLabelMetadataFitsAndOversizedCreationRollsBack(t *testing.T) {
	store := testStore(t)
	cfg := testConfig()
	cfg.Labels = map[string]string{}
	for _, key := range []string{"a", "b", "c", "d"} {
		cfg.Labels[key] = strings.Repeat("\\", 4095)
	}
	record, err := store.Create(context.Background(), cfg, "escaped-labels")
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(context.Background(), record.ID)
	if err != nil || !reflect.DeepEqual(got.Labels, cfg.Labels) {
		t.Fatalf("escaped labels %v %v", got.Labels, err)
	}
	cfg.Command = []string{strings.Repeat("x", 33*1024)}
	if _, err := store.Create(context.Background(), cfg, "oversized-record"); err == nil {
		t.Fatal("accepted oversized record")
	}
	entries, err := os.ReadDir(store.root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".create-") {
			t.Fatalf("staging leaked: %s", entry.Name())
		}
	}
	listed, err := store.List(context.Background())
	if err != nil || len(listed) != 1 || listed[0].ID != record.ID {
		t.Fatalf("rollback %v %v", listed, err)
	}
}
