//go:build linux

package container

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func scanReport(t *testing.T) func(string, error) {
	t.Helper()
	return func(id string, err error) { t.Errorf("scan %s: %v", id, err) }
}

func TestRestartScannerBoundsStateReadsAndCachesOnlyPolicies(t *testing.T) {
	store, records := paginationStore(t, 256)
	states := watchRecordReads(t, store, records)
	configs := watchContainerFileReads(t, store, records, "config.json")
	scan := restartScanner{}
	for cycle := range 2 {
		for page := range 4 {
			ids, err := scan.next(context.Background(), store, nil, 4, scanReport(t))
			if err != nil || len(ids) != 0 {
				t.Fatalf("cycle=%d page=%d: %v %v", cycle, page, ids, err)
			}
			wantStates, wantConfigs := 64, 0
			if cycle == 0 {
				wantStates, wantConfigs = 128, 64
			}
			if got := states(); got != wantStates {
				t.Fatalf("state reads=%d want %d", got, wantStates)
			}
			if got := configs(); got != wantConfigs {
				t.Fatalf("config reads=%d want %d", got, wantConfigs)
			}
		}
	}
	if len(scan.policies) != 256 || len(scan.pending) != 0 {
		t.Fatal("unexpected cached state")
	}
	// Retained config is immutable. A warm scan must not reopen its payload.
	if err := os.WriteFile(filepath.Join(store.root, records[0].ID, "config.json"), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	configs() // Discard the fixture writer open event.
	if _, err := scan.next(context.Background(), store, nil, 4, scanReport(t)); err != nil {
		t.Fatal(err)
	}
	if got := configs(); got != 0 {
		t.Fatalf("warm scan reread immutable configs: %d", got)
	}
}

func TestRestartScannerKeepsPendingCandidatesAndPrunesRemovedIDs(t *testing.T) {
	store, records := paginationStore(t, 12)
	for _, record := range records {
		cfg := testConfig()
		cfg.RestartPolicy = "always"
		if err := store.writeJSON(filepath.Join(store.root, record.ID), "config.json", cfg, maxConfigBytes); err != nil {
			t.Fatal(err)
		}
	}
	scan := restartScanner{}
	active := map[string]bool{records[0].ID: true}
	seen := map[string]bool{}
	for i := range 3 {
		ids, err := scan.next(context.Background(), store, active, 4, scanReport(t))
		if err != nil {
			t.Fatal(err)
		}
		if i < 2 && len(ids) != 4 {
			t.Fatalf("pending candidates lost: %v", ids)
		}
		for _, id := range ids {
			if active[id] || seen[id] {
				t.Fatalf("duplicate/active candidate %s", id)
			}
			seen[id] = true
		}
	}
	if len(seen) != 11 {
		t.Fatalf("starved candidates: %d", len(seen))
	}
	if err := os.RemoveAll(filepath.Join(store.root, records[1].ID)); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := scan.next(context.Background(), store, active, 4, scanReport(t)); err != nil {
			t.Fatal(err)
		}
	}
	if _, found := scan.policies[records[1].ID]; found {
		t.Fatal("removed identity retained in cache")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := scan.next(ctx, store, nil, 4, scanReport(t)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestRestartScannerContinuesAfterMissingCandidateAndRespectsCapacity(t *testing.T) {
	store, records := paginationStore(t, 2)
	cfg := testConfig()
	cfg.RestartPolicy = "always"
	if err := store.writeJSON(filepath.Join(store.root, records[1].ID), "config.json", cfg, maxConfigBytes); err != nil {
		t.Fatal(err)
	}
	scan := restartScanner{pending: records, end: true}
	if err := os.RemoveAll(filepath.Join(store.root, records[0].ID)); err != nil {
		t.Fatal(err)
	}
	reports := 0
	ids, err := scan.next(context.Background(), store, nil, 1, func(_ string, err error) {
		reports++
		if !errors.Is(err, ErrNotFound) {
			t.Error(err)
		}
	})
	if err != nil || len(ids) != 1 || ids[0] != records[1].ID || reports != 1 {
		t.Fatalf("ids=%v reports=%d: %v", ids, reports, err)
	}
	// A full worker pool must not even enumerate the store.
	if _, err := scan.next(context.Background(), nil, nil, 0, scanReport(t)); err != nil {
		t.Fatal(err)
	}
}
