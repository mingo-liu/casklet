//go:build linux

package container

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func finishTestExecution(t *testing.T, store *Store, record Record, code int) {
	t.Helper()
	if err := store.Complete(context.Background(), record.ID, record.Generation, func(record *Record) {
		now := time.Now().UTC()
		record.State = StateExited
		record.StartedAt = &now
		record.FinishedAt = &now
		record.ExitCode = &code
	}); err != nil {
		t.Fatal(err)
	}
}

func TestExecutionReceiptsSurviveLaterStarts(t *testing.T) {
	store := testStore(t)
	record := createTestRecord(t, store, "lifecycle")
	finishTestExecution(t, store, record, 7)
	operation, err := store.AcquireOperation(context.Background(), record.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer operation.Close()
	next, err := store.BeginExecution(context.Background(), record.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if next.Generation != 1 || next.PreviousExit == nil || next.PreviousExit.ExitCode == nil || *next.PreviousExit.ExitCode != 7 || next.ExitCode != nil || next.FinishedAt != nil || next.State != StateStarting || next.CreatedAt != record.CreatedAt {
		t.Fatalf("new execution=%+v", next)
	}
	first, done, err := store.Completion(context.Background(), record.ID, 0)
	if err != nil || !done || first.ExitCode == nil || *first.ExitCode != 7 {
		t.Fatalf("previous completion=%+v done=%v error=%v", first, done, err)
	}
	if _, done, err := store.Completion(context.Background(), record.ID, 1); err != nil || done {
		t.Fatalf("new execution falsely completed: %v %v", done, err)
	}
	if err := store.Complete(context.Background(), record.ID, 0, func(record *Record) { record.State = StateFailed }); err == nil {
		t.Fatal("stale completion overwrote current execution")
	}
	finishTestExecution(t, store, next, 9)
	first, done, err = store.Completion(context.Background(), record.ID, 0)
	if err != nil || !done || *first.ExitCode != 7 {
		t.Fatal("later completion lost previous status")
	}
	second, done, err := store.Completion(context.Background(), record.ID, 1)
	if err != nil || !done || second.ExitCode == nil || *second.ExitCode != 9 {
		t.Fatalf("second completion=%+v done=%v error=%v", second, done, err)
	}
}

func TestCompletionRecoversAlreadyPublishedReceipt(t *testing.T) {
	store := testStore(t)
	record := createTestRecord(t, store, "receipt-recovery")
	code := 11
	now := time.Now().UTC()
	saved := ExecutionResult{CleanupFailures: []string{"cgroup.remove"}, Generation: 0, StartedAt: &now, FinishedAt: &now, ExitCode: &code}
	if err := store.writeJSON(filepath.Join(store.root, record.ID), receiptName(0), saved, maxRecordBytes); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(context.Background(), record.ID, 0, func(record *Record) { record.State = StateFailed; record.FinishedAt = &now; record.ExitCode = nil }); err != nil {
		t.Fatal(err)
	}
	recovered, err := store.Get(context.Background(), record.ID)
	if err != nil || recovered.ExitCode == nil || *recovered.ExitCode != 11 || recovered.State != StateExited || len(recovered.CleanupFailures) != 1 || recovered.CleanupFailures[0] != "cgroup.remove" {
		t.Fatalf("receipt recovery=%+v error=%v", recovered, err)
	}
}

func TestCleanupFailuresSurviveRestartInExecutionReceipt(t *testing.T) {
	store := testStore(t)
	record := createTestRecord(t, store, "cleanup-receipt")
	code := 7
	if err := store.Complete(context.Background(), record.ID, 0, func(r *Record) {
		now := time.Now().UTC()
		r.State, r.StartedAt, r.FinishedAt, r.ExitCode = StateExited, &now, &now, &code
		r.CleanupFailures = []string{"cgroup.remove"}
		r.Error = "cleanup cgroup.remove: private path"
	}); err != nil {
		t.Fatal(err)
	}
	operation, err := store.AcquireOperation(context.Background(), record.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer operation.Close()
	next, err := store.BeginExecution(context.Background(), record.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.CleanupFailures) != 0 || next.Error != "" || next.PreviousExit == nil || len(next.PreviousExit.CleanupFailures) != 1 {
		t.Fatalf("restart did not separate cleanup results: %+v", next)
	}
	result, done, err := store.Completion(context.Background(), record.ID, 0)
	if err != nil || !done || result.ExitCode == nil || *result.ExitCode != 7 || len(result.CleanupFailures) != 1 {
		t.Fatalf("cleanup completion lost: %+v done=%v err=%v", result, done, err)
	}
}

func TestLifecycleOperationLockDoesNotBlockReaders(t *testing.T) {
	store := testStore(t)
	record := createTestRecord(t, store, "operation-lock")
	operation, err := store.AcquireOperation(context.Background(), record.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer operation.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := store.Get(ctx, record.ID); err != nil {
		t.Fatal("operation blocked readers:", err)
	}
	if _, err := store.AcquireOperation(ctx, record.ID); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("operation lock ignored cancellation: %v", err)
	}
}

func TestBeginExecutionRequiresInactiveSupervisor(t *testing.T) {
	store := testStore(t)
	record := createTestRecord(t, store, "leased-execution")
	if _, err := store.BeginExecution(context.Background(), record.ID, 0); err == nil {
		t.Fatal("started an active container")
	}
	finishTestExecution(t, store, record, 0)
	lease, err := store.AcquireLease(context.Background(), record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginExecution(context.Background(), record.ID, 0); !errors.Is(err, ErrBusy) {
		t.Fatalf("started with active supervisor lease: %v", err)
	}
	lease.Close()
	if _, err := store.BeginExecution(context.Background(), record.ID, 4); err == nil {
		t.Fatal("accepted stale generation")
	}
}

func TestRetainedRootFSRemovalDoesNotFollowSymlinks(t *testing.T) {
	store := testStore(t)
	record := createTestRecord(t, store, "retained-filesystem")
	root, err := store.RootFS(context.Background(), record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	marker := filepath.Join(outside, "keep")
	if err := os.WriteFile(marker, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "external")); err != nil {
		t.Fatal(err)
	}
	finishTestExecution(t, store, record, 0)
	if err := store.Remove(context.Background(), record.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("removal traversed retained symlink")
	}
}
