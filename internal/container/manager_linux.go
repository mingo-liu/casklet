//go:build linux

package container

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/mingo-liu/casklet/internal/config"
	containerruntime "github.com/mingo-liu/casklet/internal/runtime"
	"github.com/mingo-liu/casklet/internal/template"
	"github.com/mingo-liu/casklet/internal/volume"
)

const (
	detachedStartupLimit = 95 * time.Second
	managementStopLimit  = 35 * time.Second
)

func managementStore() (*Store, error) {
	if err := checkSystemd(); err != nil {
		return nil, err
	}
	return OpenStore()
}

// Start schedules an independent supervisor and waits for durable command startup.
// Even a command that immediately exits successfully receives a retained record.
func Start(ctx context.Context, cfg config.Config, name string) (Record, error) {
	if err := cfg.ValidateExecution(); err != nil {
		return Record{}, err
	}
	store, err := managementStore()
	if err != nil {
		return Record{}, err
	}
	volumes, err := volume.AcquireMounts(ctx, cfg.Mounts)
	if err != nil {
		return Record{}, err
	}
	defer volumes.Close()
	source, err := template.Acquire(ctx, cfg)
	if err != nil {
		return Record{}, err
	}
	defer source.Close()
	cfg.RootFS = source.Path
	ctx, cancel := context.WithTimeout(ctx, detachedStartupLimit)
	defer cancel()
	record, err := store.Create(ctx, cfg, name)
	if err != nil {
		return Record{}, err
	}
	id := record.ID
	operation, err := store.AcquireOperation(ctx, id)
	if err != nil {
		return record, rollbackStart(store, id, record.Generation, err)
	}
	defer operation.Close()
	if err := store.Update(ctx, id, func(record *Record) error {
		record.State = StateStarting
		return nil
	}); err != nil {
		return record, rollbackStart(store, id, record.Generation, err)
	}
	record.State = StateStarting
	return launchExecution(ctx, store, record)
}

func launchExecution(ctx context.Context, store *Store, record Record) (Record, error) {
	id := record.ID
	generation := record.Generation
	exe, err := os.Executable()
	if err != nil {
		return record, rollbackStart(store, id, generation, err)
	}
	args := []string{"--quiet", "--service-type=exec", "--unit=" + unitName(id, record.Generation),
		"--property=Delegate=memory pids cpu", "--property=KillMode=mixed", "--property=TimeoutStopSec=95s",
		"--property=Restart=no", "--property=StandardInput=null", "--property=StandardOutput=null",
		"--property=StandardError=null", "--working-directory=/", "--", exe, "__supervise", id, strconv.FormatUint(record.Generation, 10)}
	if _, err := systemdCommand(ctx, "systemd-run", args...); err != nil {
		return record, rollbackStart(store, id, generation, err)
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		record, err = store.Get(ctx, id)
		if err == nil && record.Generation != generation {
			return record, errors.New("container execution changed during startup")
		}
		if err == nil {
			record, err = refreshRecord(ctx, store, record, true)
		}
		if err != nil {
			return record, rollbackStart(store, id, generation, err)
		}
		if record.StartedAt != nil {
			return record, nil
		}
		if record.Terminal() {
			return record, fmt.Errorf("container %s failed to start: %s", record.ID, record.Error)
		}
		select {
		case <-ctx.Done():
			return record, rollbackStart(store, id, generation, ctx.Err())
		case <-ticker.C:
		}
	}
}

func rollbackStart(store *Store, id string, generation uint64, cause error) error {
	ctx, cancel := context.WithTimeout(context.Background(), managementStopLimit)
	defer cancel()
	if err := stopUnit(ctx, id, generation); err != nil {
		return errors.Join(cause, fmt.Errorf("stop failed startup %s: %w", id, err))
	}
	record, err := store.Get(ctx, id)
	if err != nil {
		return errors.Join(cause, err)
	}
	if record.Generation != generation {
		return cause
	}
	if err := containerruntime.RecoverAbandoned(ctx, os.Stderr); err != nil {
		return errors.Join(cause, err)
	}
	if record.RunPath != "" {
		if err := containerruntime.RecoverRun(ctx, record.RunPath); err != nil {
			return errors.Join(cause, err)
		}
	}
	return errors.Join(cause, store.Complete(ctx, id, generation, func(record *Record) {
		if !record.Terminal() {
			finished := time.Now().UTC()
			record.State, record.FinishedAt, record.Error = StateFailed, &finished, cause.Error()
		}
	}))
}

// refreshRecord fences a lost supervisor with its lease before reclaiming state.
// No numeric PID from disk is ever used for signaling or deletion.
func refreshRecord(ctx context.Context, store *Store, record Record, launched bool) (Record, error) {
	if record.Terminal() {
		return record, nil
	}
	status, err := inspectUnit(ctx, record.ID, record.Generation)
	if err != nil || status.live() {
		return record, err
	}
	// A concurrent run may have published its record but not scheduled its unit.
	if !launched {
		pending, err := schedulingPending(record, status)
		if err != nil || pending {
			return record, err
		}
	}
	lease, err := store.AcquireLease(ctx, record.ID)
	if errors.Is(err, ErrBusy) {
		return store.Get(ctx, record.ID)
	}
	if err != nil {
		return record, err
	}
	defer lease.Close()
	expectedGeneration := record.Generation
	record, err = store.Get(ctx, record.ID)
	if err != nil || record.Terminal() || record.Generation != expectedGeneration {
		return record, err
	}
	// Stopping the immutable unit also handles any remaining delegated children.
	if err := stopUnit(ctx, record.ID, record.Generation); err != nil {
		return record, err
	}
	// Scan safely locked working directories as well, covering interruption
	// between a directory's publication and its first durable observer update.
	if err := containerruntime.RecoverAbandoned(ctx, os.Stderr); err != nil {
		return record, err
	}
	if record.RunPath != "" {
		if err := containerruntime.RecoverRun(ctx, record.RunPath); err != nil {
			return record, fmt.Errorf("preserve container %s: %w", record.ID, err)
		}
	}
	err = store.Complete(ctx, record.ID, record.Generation, func(record *Record) {
		finished := time.Now().UTC()
		record.State, record.FinishedAt = StateFailed, &finished
		record.Error = "supervisor exited without recording completion"
		// systemd's status belongs to the supervisor, not the user command.
		// Leave the command exit code unknown after an abrupt supervisor loss.
	})
	if err != nil {
		return record, err
	}
	return store.Get(ctx, record.ID)
}

func schedulingPending(record Record, status unitStatus) (bool, error) {
	if record.StartedAt != nil {
		return false, nil
	}
	bootID, err := currentBootID()
	if err != nil {
		return false, err
	}
	return schedulingPendingAt(record, status, bootID, time.Now()), nil
}

func schedulingPendingAt(record Record, status unitStatus, bootID string, now time.Time) bool {
	// A transient unit is briefly loaded but inactive before its start job runs.
	// Readers must not claim its supervisor lease in that scheduling window.
	unscheduled := status.LoadState == "not-found" || status.LoadState == "loaded" && status.ActiveState == "inactive" && status.MainPID == 0 && status.ExitCode == 0
	if !unscheduled || record.StartedAt != nil || (record.State != StateCreated && record.State != StateStarting) {
		return false
	}
	if record.BootID != "" && record.BootID != bootID {
		return false
	}
	launchedAt := record.CreatedAt
	if record.LaunchAt != nil {
		launchedAt = *record.LaunchAt
	}
	age := now.Sub(launchedAt)
	// A previous boot or a backwards wall-clock jump must not make an absent
	// supervisor look like an in-progress launch indefinitely.
	return age >= 0 && age < detachedStartupLimit
}

func List(ctx context.Context, all bool) ([]Record, error) {
	store, err := managementStore()
	if err != nil {
		return nil, err
	}
	records, err := store.List(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]Record, 0, len(records))
	for _, record := range records {
		record, err = refreshRecord(ctx, store, record, false)
		if errors.Is(err, ErrNotFound) {
			continue // Another CLI removed a completed record after the snapshot.
		}
		if err != nil {
			return nil, err
		}
		if all || !record.Terminal() {
			record.Health = record.effectiveHealth()
			result = append(result, record)
		}
	}
	return result, nil
}

// Stop lets the runtime forward SIGTERM and clean up; systemd enforces a final
// whole-service kill boundary if the supervisor cannot complete its shutdown.
func stopLocked(ctx context.Context, store *Store, record Record, override *time.Duration) (Record, error) {
	cfg, err := store.Config(ctx, record.ID)
	if err != nil {
		return record, err
	}
	grace := cfg.StoppingTimeout()
	if override != nil {
		grace = *override
	}
	ctx, cancel := context.WithTimeout(ctx, grace+managementStopLimit)
	defer cancel()
	status, err := inspectUnit(ctx, record.ID, record.Generation)
	if err != nil {
		return record, err
	}
	if !record.Terminal() {
		pending, err := schedulingPending(record, status)
		if err != nil {
			return record, err
		}
		if pending {
			return record, errors.New("container is still being scheduled; retry stop after startup completes")
		}
	}
	if !record.Terminal() {
		if err := store.Update(ctx, record.ID, func(record *Record) error {
			if !record.Terminal() {
				record.State = StateStopping
				record.StopTimeout = &grace
			}
			return nil
		}); err != nil {
			return record, err
		}
	}
	// systemd cannot change TimeoutStopSec on an existing service. Its fixed
	// boundary allows the maximum configurable grace plus cleanup; the runtime
	// enforces the selected per-operation timeout itself.
	if err := stopUnit(ctx, record.ID, record.Generation); err != nil {
		return record, err
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		record, err = store.Get(ctx, record.ID)
		if err == nil {
			record, err = refreshRecord(ctx, store, record, true)
		}
		if err != nil {
			return record, err
		}
		if record.Terminal() {
			if record.RunPath != "" {
				if err := containerruntime.RecoverRun(ctx, record.RunPath); err != nil {
					return record, err
				}
			}
			return record, nil
		}
		select {
		case <-ctx.Done():
			return record, ctx.Err()
		case <-ticker.C:
		}
	}
}

func removeLocked(ctx context.Context, store *Store, record Record) error {
	var err error
	record, err = refreshRecord(ctx, store, record, false)
	if err != nil {
		return err
	}
	if !record.Terminal() {
		return ErrNotTerminal
	}
	status, err := inspectUnit(ctx, record.ID, record.Generation)
	if err != nil {
		return err
	}
	if status.live() {
		return ErrBusy
	}
	if record.RunPath != "" {
		if err := containerruntime.RecoverRun(ctx, record.RunPath); err != nil {
			return err
		}
	}
	if status.LoadState != "not-found" && status.ActiveState == "failed" {
		if _, err := systemdCommand(ctx, "systemctl", "reset-failed", unitName(record.ID, record.Generation)); err != nil {
			return err
		}
	}
	return store.Remove(ctx, record.ID)
}

func Logs(ctx context.Context, ref string, tail int, follow bool, out io.Writer) error {
	if tail < -1 || tail > 1000000 {
		return errors.New("log tail must be between 0 and 1000000, or -1 for the entire log")
	}
	store, err := managementStore()
	if err != nil {
		return err
	}
	record, err := store.Get(ctx, ref)
	if err != nil {
		return err
	}
	generation := record.Generation
	cfg, err := store.Config(ctx, record.ID)
	if err != nil {
		return err
	}
	data, cursor, err := store.logSnapshot(ctx, record.ID, cfg, logCursor{}, generation)
	if err != nil {
		return err
	}
	defer func() {
		if cursor.pin != nil {
			cursor.pin.Close()
		}
	}()
	if err := copyInitialLog(bytes.NewReader(data), int64(len(data)), tail, out); err != nil || !follow {
		return err
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		data, next, err := store.logSnapshot(ctx, record.ID, cfg, cursor, generation)
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		cursor = next
		if _, err := io.Copy(out, bytes.NewReader(data)); err != nil {
			return err
		}
		record, err = store.Get(ctx, record.ID)
		if errors.Is(err, ErrNotFound) {
			return nil // The captured snapshot remains valid after concurrent removal.
		}
		if err != nil {
			return err
		}
		record, err = refreshRecord(ctx, store, record, false)
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if record.Terminal() || record.Generation != generation {
			data, next, err := store.logSnapshot(ctx, record.ID, cfg, cursor, generation)
			cursor = next
			if errors.Is(err, ErrNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
			_, err = io.Copy(out, bytes.NewReader(data))
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
