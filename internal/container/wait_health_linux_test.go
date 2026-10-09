//go:build linux

package container

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestHealthWaitDeadlineBoundsInitialSnapshotLock(t *testing.T) {
	store := testStore(t)
	record := createTestRecord(t, store, "blocked-readiness")
	lock, err := store.lock(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	started := time.Now()
	err = withHealthWaitDeadline(context.Background(), 20*time.Millisecond, func(ctx context.Context) error {
		_, _, err := store.Snapshot(ctx, record.ID)
		return err
	})
	if !errors.Is(err, ErrWaitTimeout) || time.Since(started) > time.Second {
		t.Fatalf("blocked snapshot: %v after %s", err, time.Since(started))
	}
}

func TestReadinessStoreOpeningHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := openStoreContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("opening after cancellation: %v", err)
	}
}
