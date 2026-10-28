//go:build linux

package container

import (
	"context"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const maxEventJournalBytes int64 = 1 << 20
const eventJournalName = ".events.json"
const eventFailureName = ".events-error"

var eventProjectName = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

type eventJournal struct {
	Version int     `json:"version"`
	Last    uint64  `json:"last"`
	Events  []Event `json:"events"`
}

type eventSnapshot struct {
	state, health, cleanup, restartAt string
	generation                        uint64
	stopped                           bool
}

func snapshotEvents(record Record) eventSnapshot {
	health := ""
	if current := record.effectiveHealth(); current != nil {
		health = current.Status
	}
	restartAt := ""
	if record.RestartAt != nil {
		restartAt = record.RestartAt.Format(time.RFC3339Nano)
	}
	return eventSnapshot{record.State, health, strings.Join(record.CleanupFailures, ","), restartAt, record.Generation, record.StoppedByUser}
}
func lifecycleEvents(before eventSnapshot, record Record) []Event {
	after := snapshotEvents(record)
	var kinds []string
	if before.state == "" {
		kinds = append(kinds, "create")
	} else {
		if before.generation != after.generation {
			kinds = append(kinds, "restart")
		}
		if before.state != after.state {
			kind := map[string]string{StateStarting: "starting", StateRunning: "start", StateStopping: "stopping", StateExited: "exit", StateFailed: "exit"}[after.state]
			if kind != "" {
				kinds = append(kinds, kind)
			}
		}
		if !before.stopped && after.stopped {
			kinds = append(kinds, "stop_requested")
		}
		if after.restartAt != "" && after.restartAt != before.restartAt {
			kinds = append(kinds, "restart_scheduled")
		}
		if after.health != "" && before.health != after.health {
			kinds = append(kinds, "health")
		}
		if after.cleanup != "" && before.cleanup != after.cleanup {
			kinds = append(kinds, "cleanup_failed")
		}
	}
	events := make([]Event, 0, len(kinds))
	for _, kind := range kinds {
		events = append(events, newLifecycleEvent(record, kind))
	}
	return events
}
func newLifecycleEvent(record Record, kind string) Event {
	event := Event{SchemaVersion: 1, Time: time.Now().UTC(), ID: record.ID, Name: record.Name, Type: kind, Generation: record.Generation, State: record.State, ExitCode: record.ExitCode, ExitReason: record.ExitReason, CleanupFailures: append([]string(nil), record.CleanupFailures...)}
	if current := record.effectiveHealth(); current != nil {
		event.Health = current.Status
	}
	for key, target := range map[string]*string{"io.casklet.project.name": &event.Project, "io.casklet.project.service": &event.Service} {
		if value := record.Labels[key]; eventProjectName.MatchString(value) {
			*target = value
		}
	}
	return event
}
func (store *Store) readEventsLocked() (eventJournal, error) {
	journal := eventJournal{Version: 1, Events: []Event{}}
	err := store.readJSON(filepath.Join(store.root, eventJournalName), maxEventJournalBytes, &journal)
	if errors.Is(err, os.ErrNotExist) {
		return eventJournal{Version: 1, Events: []Event{}}, nil
	}
	if err != nil {
		return journal, err
	}
	if journal.Version != 1 || len(journal.Events) > EventHistoryLimit || len(journal.Events) == 0 && journal.Last != 0 {
		return journal, errors.New("invalid event journal")
	}
	previous := uint64(0)
	for i, event := range journal.Events {
		if event.SchemaVersion != 1 || validateID(event.ID) != nil || ValidateName(event.Name) != nil || event.Time.IsZero() || !eventTypes[event.Type] || event.Sequence == 0 || i > 0 && event.Sequence != previous+1 || event.ExitCode != nil && (*event.ExitCode < 0 || *event.ExitCode > 255) || validateCleanupFailures(event.CleanupFailures) != nil {
			return journal, errors.New("invalid event journal entry")
		}
		switch event.State {
		case StateCreated, StateStarting, StateRunning, StateStopping, StateExited, StateFailed:
		default:
			return journal, errors.New("invalid event state")
		}
		switch event.Health {
		case "", HealthStarting, HealthHealthy, HealthUnhealthy, HealthStopped:
		default:
			return journal, errors.New("invalid event health")
		}
		if event.Project != "" && !eventProjectName.MatchString(event.Project) || event.Service != "" && !eventProjectName.MatchString(event.Service) {
			return journal, errors.New("invalid event project identity")
		}
		if err := validateExitStatus(event.ExitReason, event.ExitReason == "oom_killed"); err != nil {
			return journal, errors.New("invalid event exit reason")
		}
		previous = event.Sequence
	}
	if len(journal.Events) > 0 && previous != journal.Last {
		return journal, errors.New("invalid event journal sequence")
	}
	return journal, nil
}

// appendEventsLocked is called only under the container metadata lock, after
// state publication. The observation wrapper records failures without blocking
// the lifecycle operation.
func (store *Store) appendEventsLocked(events []Event) error {
	if len(events) == 0 {
		return nil
	}
	journal, err := store.readEventsLocked()
	if err != nil {
		return err
	}
	for _, event := range events {
		if journal.Last == math.MaxUint64 {
			return errors.New("event sequence is exhausted")
		}
		journal.Last++
		event.Sequence = journal.Last
		journal.Events = append(journal.Events, event)
	}
	if len(journal.Events) > EventHistoryLimit {
		journal.Events = journal.Events[len(journal.Events)-EventHistoryLimit:]
	}
	return store.writeJSON(store.root, eventJournalName, journal, maxEventJournalBytes)
}

// Observation must not prevent stopping workloads or reclaiming resources.
// Keep a durable incomplete-history marker when possible; never truncate an
// unsafe artifact, and leave operator acknowledgement explicit.
func (store *Store) publishEventsLocked(events []Event) {
	if err := store.appendEventsLocked(events); err != nil {
		marker, markerErr := store.openFile(filepath.Join(store.root, eventFailureName), unix.O_WRONLY, true)
		if markerErr == nil {
			markerErr = marker.Truncate(0)
			if markerErr == nil {
				_, markerErr = marker.WriteString("incomplete lifecycle event history\n")
			}
			if markerErr == nil {
				markerErr = marker.Sync()
			}
			marker.Close()
			if markerErr == nil {
				markerErr = syncDirectory(store.root)
			}
		}
		fmt.Fprintf(os.Stderr, "casklet: lifecycle event recording failed; history may be incomplete: %v\n", errors.Join(err, markerErr))
	}
}

func (store *Store) writeStateEvents(id string, before eventSnapshot, record Record) error {
	if err := store.writeJSON(filepath.Join(store.root, id), "state.json", record, maxRecordBytes); err != nil {
		return err
	}
	store.publishEventsLocked(lifecycleEvents(before, record))
	return nil
}

// Events replays bounded retained history and optionally follows future changes.
// It releases storage locks before calling emit, including with blocked stdout.
func Events(ctx context.Context, query EventQuery, emit func(Event) error) error {
	if err := query.Validate(); err != nil {
		return err
	}
	store, err := OpenStoreContext(ctx)
	if err != nil {
		return err
	}
	return store.events(ctx, query, emit)
}
func (store *Store) events(ctx context.Context, query EventQuery, emit func(Event) error) error {
	if err := query.Validate(); err != nil {
		return err
	}
	cursor := uint64(0)
	initialized := false
	observed := false
	if query.After != nil {
		cursor = *query.After
		initialized = true
	}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		lock, err := store.lock(ctx, true)
		if err != nil {
			return err
		}
		marker, markerErr := store.openFile(filepath.Join(store.root, eventFailureName), unix.O_RDONLY, false)
		if markerErr == nil {
			marker.Close()
			lock.Close()
			return errors.New("lifecycle event history is incomplete after a recording failure; inspect VM storage before resetting history")
		}
		if !errors.Is(markerErr, os.ErrNotExist) {
			lock.Close()
			return markerErr
		}
		journal, err := store.readEventsLocked()
		lock.Close()
		if err != nil {
			return err
		}
		if !initialized {
			if query.Follow && query.Since.IsZero() {
				cursor = journal.Last
			}
			initialized = true
		}
		if cursor > journal.Last {
			return errors.New("event cursor is newer than this store; retry without --after")
		}
		if (cursor > 0 || observed) && len(journal.Events) > 0 && cursor < journal.Events[0].Sequence-1 {
			return errors.New("event history expired before the cursor; retry from retained history")
		}
		for _, event := range journal.Events {
			if err := ctx.Err(); err != nil {
				return err
			}
			if event.Sequence <= cursor {
				continue
			}
			cursor = event.Sequence
			if query.matches(event) {
				if err := emit(event); err != nil {
					return err
				}
			}
		}
		observed = true
		if !query.Follow {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
