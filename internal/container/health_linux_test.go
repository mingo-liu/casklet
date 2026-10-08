//go:build linux

package container

import (
	"context"
	"errors"
	"testing"
)

func TestHealthResultsCannotCrossExecutionsOrStopping(t *testing.T) {
	store := testStore(t)
	record := createTestRecord(t, store, "health")
	ctx := context.Background()
	health := Health{Status: HealthHealthy, Checks: []HealthResult{}}
	if err := store.Update(ctx, record.ID, func(r *Record) error { r.State = StateRunning; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := store.updateHealth(ctx, record.ID, record.Generation, health); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(ctx, record.ID, func(r *Record) error { r.State = StateStopping; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := store.updateHealth(ctx, record.ID, record.Generation, health); !errors.Is(err, errHealthInactive) {
		t.Fatal(err)
	}
	finishTestExecution(t, store, record, 0)
	operation, err := store.AcquireOperation(ctx, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer operation.Close()
	next, err := store.BeginExecution(ctx, record.ID, record.Generation)
	if err != nil {
		t.Fatal(err)
	}
	if next.Health != nil {
		t.Fatal("carried old probe results into new execution")
	}
	store.Update(ctx, record.ID, func(r *Record) error { r.State = StateRunning; return nil })
	if err := store.updateHealth(ctx, record.ID, record.Generation, health); !errors.Is(err, errHealthInactive) {
		t.Fatal(err)
	}
	next, err = store.Get(ctx, record.ID)
	if err != nil || next.Health != nil {
		t.Fatalf("stale result wrote new execution: %+v %v", next, err)
	}
}
