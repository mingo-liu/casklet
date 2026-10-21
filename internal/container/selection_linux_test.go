//go:build linux

package container

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestListIDsReadsOnlyPinnedRecordsAndPreservesRefresh(t *testing.T) {
	store, records := paginationStore(t, 256)
	reads := watchRecordReads(t, store, records)
	ids := []string{records[255].ID, records[0].ID}
	calls := []string{}
	values, err := listIDs(context.Background(), store, ids, func(ctx context.Context, record Record) (Record, error) {
		calls = append(calls, record.ID)
		record.Health = &Health{Status: HealthHealthy}
		return record, nil
	})
	count := reads()
	if err != nil || len(values) != 2 || count != 2 || !slices.Equal(calls, ids) {
		t.Fatalf("selection: records=%d reads=%d calls=%v err=%v", len(values), count, calls, err)
	}
	for i, value := range values {
		if value.ID != ids[i] || value.Health == nil || value.Health.Status != HealthStopped {
			t.Fatalf("changed identity or ineffective health: %+v", value)
		}
	}
	// Recovery can update metadata only after Get releases its shared lock.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := listIDs(ctx, store, ids, func(ctx context.Context, record Record) (Record, error) {
		return record, store.Update(ctx, record.ID, func(*Record) error { return nil })
	}); err != nil {
		t.Fatalf("refresh kept the metadata lock: %v", err)
	}
	if err := os.WriteFile(filepath.Join(store.root, records[1].ID, "state.json"), []byte("invalid JSON"), 0600); err != nil {
		t.Fatal(err)
	}
	reads = watchRecordReads(t, store, records)
	values, err = listIDs(context.Background(), store, ids, func(_ context.Context, record Record) (Record, error) { return record, nil })
	if err != nil || len(values) != 2 || reads() != 2 {
		t.Fatalf("unrelated state affected selection: %d %v", len(values), err)
	}
	if _, err := listIDs(context.Background(), store, []string{records[1].ID}, nil); err == nil {
		t.Fatal("invalid selected state was ignored")
	}
}

func TestListIDsRejectsInvalidSelectionsBeforeReading(t *testing.T) {
	store, records := paginationStore(t, 1)
	reads := watchRecordReads(t, store, records)
	for _, ids := range [][]string{nil, {records[0].ID, "bad"}, {records[0].ID[:12]}, {records[0].Name}, {records[0].ID, records[0].ID}, make([]string, 65)} {
		if _, err := listIDs(context.Background(), store, ids, nil); err == nil || reads() != 0 {
			t.Fatalf("invalid selection read state: %v %v", ids, err)
		}
	}
}

func TestListIDsOmitsRemovedIDsAndHonorsCancellationAndErrors(t *testing.T) {
	store, records := paginationStore(t, 2)
	ids := []string{records[0].ID, strings.Repeat("f", 32), records[1].ID}
	calls := 0
	values, err := listIDs(context.Background(), store, ids, func(_ context.Context, record Record) (Record, error) {
		calls++
		if record.ID == records[0].ID {
			return Record{}, ErrNotFound
		}
		return record, nil
	})
	if err != nil || calls != 2 || len(values) != 1 || values[0].ID != records[1].ID {
		t.Fatalf("removed IDs: records=%d calls=%d err=%v", len(values), calls, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reads := watchRecordReads(t, store, records)
	if _, err := listIDs(ctx, store, ids, nil); !errors.Is(err, context.Canceled) || reads() != 0 {
		t.Fatalf("pre-canceled selection: %v", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	_, err = listIDs(ctx, store, ids, func(_ context.Context, record Record) (Record, error) { cancel(); return record, nil })
	if !errors.Is(err, context.Canceled) || reads() != 1 {
		t.Fatalf("cancellation during refresh: %v", err)
	}
	cause := errors.New("refresh failed")
	if _, err := listIDs(context.Background(), store, ids, func(context.Context, Record) (Record, error) { return Record{}, cause }); !errors.Is(err, cause) {
		t.Fatalf("refresh error lost: %v", err)
	}
}
