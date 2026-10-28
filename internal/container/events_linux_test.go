//go:build linux

package container

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func collectEvents(t *testing.T, store *Store, query EventQuery) []Event {
	t.Helper()
	var events []Event
	if err := store.events(context.Background(), query, func(e Event) error { events = append(events, e); return nil }); err != nil {
		t.Fatal(err)
	}
	return events
}
func TestEventsObserveLifecycleAndRedactPrivateValues(t *testing.T) {
	store := testStore(t)
	record := createTestRecord(t, store, "observed")
	if err := store.Update(context.Background(), record.ID, func(r *Record) error {
		r.State = StateRunning
		r.Error = "private-error"
		r.RunPath = "/private/run"
		r.Command = []string{"sensitive-command"}
		r.Health = &Health{Status: HealthHealthy}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	events := collectEvents(t, store, EventQuery{})
	if len(events) != 3 || events[0].Type != "create" || events[1].Type != "start" || events[2].Type != "health" {
		t.Fatalf("events %+v", events)
	}
	data, _ := json.Marshal(events)
	for _, private := range []string{"private-error", "/private/run", "sensitive-command"} {
		if strings.Contains(string(data), private) {
			t.Fatal("private event data leaked")
		}
	}
	if err := store.Update(context.Background(), record.ID, func(r *Record) error { r.Health = &Health{Status: HealthHealthy}; return nil }); err != nil {
		t.Fatal(err)
	}
	if got := collectEvents(t, store, EventQuery{}); len(got) != 3 {
		t.Fatal("repeated health emitted a transition")
	}
	if err := store.Update(context.Background(), record.ID, func(r *Record) error { r.State = StateStopping; return nil }); err != nil {
		t.Fatal(err)
	}
	stopped := collectEvents(t, store, EventQuery{})
	if stopped[len(stopped)-1].Type != "health" || stopped[len(stopped)-1].Health != HealthStopped || stopped[len(stopped)-2].Health != HealthStopped {
		t.Fatal("stopping events retained live health")
	}
	code := 0
	if err := store.Complete(context.Background(), record.ID, record.Generation, func(r *Record) { r.State = StateExited; r.ExitCode = &code; r.ExitReason = "exited" }); err != nil {
		t.Fatal(err)
	}
	if err := store.Remove(context.Background(), record.ID); err != nil {
		t.Fatal(err)
	}
	events = collectEvents(t, store, EventQuery{Filters: []EventFilter{{"container", record.ID}}})
	if events[len(events)-1].Type != "remove" {
		t.Fatal("missing removal history")
	}
	for i, event := range events {
		if event.Sequence != uint64(i+1) {
			t.Fatalf("sequence %+v", events)
		}
	}
}
func TestEventRetentionAndResumeDetectGaps(t *testing.T) {
	store := testStore(t)
	record := createTestRecord(t, store, "retention")
	events := make([]Event, EventHistoryLimit+3)
	for i := range events {
		events[i] = newLifecycleEvent(record, "start")
	}
	lock, err := store.lock(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	err = store.appendEventsLocked(events)
	lock.Close()
	if err != nil {
		t.Fatal(err)
	}
	retained := collectEvents(t, store, EventQuery{})
	if len(retained) != EventHistoryLimit || retained[0].Sequence != 5 {
		t.Fatalf("retention %d %+v", len(retained), retained[0])
	}
	after := uint64(1)
	if err := store.events(context.Background(), EventQuery{After: &after}, func(Event) error { return nil }); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("lost history %v", err)
	}
	after = retained[len(retained)-2].Sequence
	got := collectEvents(t, store, EventQuery{After: &after})
	if len(got) != 1 {
		t.Fatal("resume was not exclusive")
	}
}
func TestEventFollowReleasesMetadataLockAndCancels(t *testing.T) {
	store := testStore(t)
	record := createTestRecord(t, store, "follow")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	after := uint64(0)
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	var once sync.Once
	go func() {
		done <- store.events(ctx, EventQuery{Follow: true, After: &after}, func(Event) error { once.Do(func() { close(entered) }); <-release; return ctx.Err() })
	}()
	<-entered
	if err := store.Update(context.Background(), record.ID, func(r *Record) error { r.State = StateRunning; return nil }); err != nil {
		t.Fatal(err)
	}
	cancel()
	close(release)
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked follow")
	}
	if err := store.events(context.Background(), EventQuery{}, func(Event) error { return io.ErrClosedPipe }); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal(err)
	}
}
func TestEventsRejectUnsafeJournalWithoutBlockingLifecycle(t *testing.T) {
	store := testStore(t)
	record := createTestRecord(t, store, "unsafe")
	journal := filepath.Join(store.root, eventJournalName)
	if err := os.Remove(journal); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	os.WriteFile(outside, []byte("sentinel"), 0600)
	if err := os.Symlink(outside, journal); err != nil {
		t.Fatal(err)
	}
	err := store.Update(context.Background(), record.ID, func(r *Record) error { r.State = StateRunning; return nil })
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(context.Background(), record.ID)
	if err != nil || got.State != StateRunning {
		t.Fatalf("state %+v %v", got, err)
	}
	if err := store.events(context.Background(), EventQuery{}, func(Event) error { return nil }); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("missing history failure: %v", err)
	}
	code := 0
	if err := store.Complete(context.Background(), record.ID, record.Generation, func(r *Record) { r.State = StateExited; r.ExitCode = &code; r.ExitReason = "exited" }); err != nil {
		t.Fatal(err)
	}
	if err := store.Remove(context.Background(), record.ID); err != nil {
		t.Fatalf("journal failure prevented removal: %v", err)
	}
	if data, _ := os.ReadFile(outside); string(data) != "sentinel" {
		t.Fatal("followed journal symlink")
	}
}

func observeJournalOpen(t *testing.T, store *Store) func() {
	t.Helper()
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { unix.Close(fd) })
	if _, err := unix.InotifyAddWatch(fd, filepath.Join(store.root, eventJournalName), unix.IN_OPEN); err != nil {
		t.Fatal(err)
	}
	return func() {
		t.Helper()
		deadline := time.Now().Add(time.Second)
		var data [4096]byte
		for time.Now().Before(deadline) {
			n, err := unix.Read(fd, data[:])
			if n > 0 {
				return
			}
			if err != nil && !errors.Is(err, unix.EAGAIN) {
				t.Fatal(err)
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatal("event reader did not open its journal")
	}
}
func TestEventFollowStartsAtTailAndEmptyCursorDetectsOverrun(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(fmt.Sprint(empty), func(t *testing.T) {
			store := testStore(t)
			record := createTestRecord(t, store, "old")
			if empty {
				if err := store.writeJSON(store.root, eventJournalName, eventJournal{Version: 1, Events: []Event{}}, maxEventJournalBytes); err != nil {
					t.Fatal(err)
				}
			}
			opened := observeJournalOpen(t, store)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			received := make(chan Event, 1)
			go func() {
				done <- store.events(ctx, EventQuery{Follow: true}, func(e Event) error { received <- e; return nil })
			}()
			opened()
			if empty {
				events := make([]Event, EventHistoryLimit+2)
				for i := range events {
					events[i] = newLifecycleEvent(record, "start")
				}
				lock, err := store.lock(ctx, false)
				if err != nil {
					t.Fatal(err)
				}
				err = store.appendEventsLocked(events)
				lock.Close()
				if err != nil {
					t.Fatal(err)
				}
				select {
				case err := <-done:
					if err == nil || !strings.Contains(err.Error(), "expired") {
						t.Fatalf("overrun %v", err)
					}
				case <-time.After(time.Second):
					t.Fatal("overrun did not fail")
				}
			} else {
				next := createTestRecord(t, store, "new")
				select {
				case e := <-received:
					if e.ID != next.ID {
						t.Fatal("replayed old history")
					}
				case <-time.After(time.Second):
					t.Fatal("missing new event")
				}
				cancel()
				select {
				case err := <-done:
					if !errors.Is(err, context.Canceled) {
						t.Fatal(err)
					}
				case <-time.After(time.Second):
					t.Fatal("did not cancel follow")
				}
			}
		})
	}
}
