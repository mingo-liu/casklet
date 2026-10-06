//go:build linux

package container

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestSnapshotConcurrentRemovalAndNameReuse(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	record := createTestRecord(t, store, "worker")
	if err := store.Update(ctx, record.ID, func(r *Record) error { r.State = StateExited; return nil }); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	failures := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				got, cfg, err := store.Snapshot(ctx, record.ID)
				if errors.Is(err, ErrNotFound) {
					continue
				}
				if err != nil {
					failures <- err
					return
				}
				if got.ID != record.ID || len(cfg.Command) != 2 || cfg.Command[0] != "/bin/echo" {
					failures <- errors.New("inconsistent snapshot")
					return
				}
			}
		}()
	}
	if err := store.Remove(ctx, record.ID); err != nil {
		t.Fatal(err)
	}
	replacement := createTestRecord(t, store, "worker")
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	if _, _, err := store.Snapshot(ctx, record.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removed ID: %v", err)
	}
	got, _, err := store.Snapshot(ctx, "worker")
	if err != nil || got.ID != replacement.ID {
		t.Fatalf("new name: %+v, %v", got, err)
	}
}

func TestSnapshotDoesNotDiscloseInvalidEnvironment(t *testing.T) {
	store := testStore(t)
	record := createTestRecord(t, store, "worker")
	cfg := testConfig()
	cfg.Env = []string{"INVALID-KEY=private-invalid-value"}
	if err := store.writeJSON(filepath.Join(store.root, record.ID), "config.json", cfg, maxConfigBytes); err != nil {
		t.Fatal(err)
	}
	_, _, err := store.Snapshot(context.Background(), record.ID)
	if err == nil || strings.Contains(err.Error(), "private-invalid-value") {
		t.Fatalf("unsafe validation error: %v", err)
	}
}
