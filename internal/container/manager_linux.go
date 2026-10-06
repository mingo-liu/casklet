//go:build linux

package container

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/mingo-liu/mini-docker/internal/config"
	"github.com/mingo-liu/mini-docker/internal/image"
	"github.com/mingo-liu/mini-docker/internal/rootfs"
	containerruntime "github.com/mingo-liu/mini-docker/internal/runtime"
)

const (
	detachedStartupLimit = 95 * time.Second
	managementStopLimit  = 25 * time.Second
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
	if cfg.Image != "" {
		images, err := image.OpenStore()
		if err != nil {
			return Record{}, err
		}
		_, tree, lease, err := images.Acquire(ctx, cfg.Image)
		if err != nil {
			return Record{}, err
		}
		defer lease.Close()
		cfg.RootFS = tree
	}
	cfg.RootFS, err = rootfs.Validate(cfg.RootFS)
	if err != nil {
		return Record{}, err
	}
	if cfg.Image == "" && image.IsStorePath(cfg.RootFS) {
		return Record{}, errors.New("stored image filesystems require --image")
	}
	if err := rootfs.ValidateMountSources(cfg.Mounts, cfg.RootFS); err != nil {
		return Record{}, err
	}
	exe, err := os.Executable()
	if err != nil {
		return Record{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, detachedStartupLimit)
	defer cancel()
	record, err := store.Create(ctx, cfg, name)
	if err != nil {
		return Record{}, err
	}
	id := record.ID
	if err := store.Update(ctx, id, func(record *Record) error {
		record.State = StateStarting
		return nil
	}); err != nil {
		return record, rollbackStart(store, id, err)
	}
	args := []string{"--quiet", "--service-type=exec", "--unit=" + unitName(id),
		"--property=Delegate=memory pids cpu", "--property=KillMode=mixed", "--property=TimeoutStopSec=15s",
		"--property=Restart=no", "--property=StandardInput=null", "--property=StandardOutput=null",
		"--property=StandardError=null", "--working-directory=/", "--", exe, "__supervise", id}
	if _, err := systemdCommand(ctx, "systemd-run", args...); err != nil {
		return record, rollbackStart(store, id, err)
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		record, err = store.Get(ctx, id)
		if err == nil {
			record, err = refreshRecord(ctx, store, record, true)
		}
		if err != nil {
			return record, rollbackStart(store, id, err)
		}
		if record.StartedAt != nil {
			return record, nil
		}
		if record.Terminal() {
			return record, fmt.Errorf("container %s failed to start: %s", record.ID, record.Error)
		}
		select {
		case <-ctx.Done():
			return record, rollbackStart(store, id, ctx.Err())
		case <-ticker.C:
		}
	}
}

func rollbackStart(store *Store, id string, cause error) error {
	ctx, cancel := context.WithTimeout(context.Background(), managementStopLimit)
	defer cancel()
	if err := stopUnit(ctx, id); err != nil {
		return errors.Join(cause, fmt.Errorf("stop failed startup %s: %w", id, err))
	}
	record, err := store.Get(ctx, id)
	if err != nil {
		return errors.Join(cause, err)
	}
	if err := containerruntime.RecoverAbandoned(ctx, os.Stderr); err != nil {
		return errors.Join(cause, err)
	}
	if record.RunPath != "" {
		if err := containerruntime.RecoverRun(ctx, record.RunPath); err != nil {
			return errors.Join(cause, err)
		}
	}
	return errors.Join(cause, store.Update(ctx, id, func(record *Record) error {
		if !record.Terminal() {
			finished := time.Now().UTC()
			record.State, record.FinishedAt, record.Error = StateFailed, &finished, cause.Error()
		}
		return nil
	}))
}

// refreshRecord fences a lost supervisor with its lease before reclaiming state.
// No numeric PID from disk is ever used for signaling or deletion.
func refreshRecord(ctx context.Context, store *Store, record Record, launched bool) (Record, error) {
	if record.Terminal() {
		return record, nil
	}
	status, err := inspectUnit(ctx, record.ID)
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
	record, err = store.Get(ctx, record.ID)
	if err != nil || record.Terminal() {
		return record, err
	}
	// Stopping the immutable unit also handles any remaining delegated children.
	if err := stopUnit(ctx, record.ID); err != nil {
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
	err = store.Update(ctx, record.ID, func(record *Record) error {
		finished := time.Now().UTC()
		record.State, record.FinishedAt = StateFailed, &finished
		record.Error = "supervisor exited without recording completion"
		// systemd's status belongs to the supervisor, not the user command.
		// Leave the command exit code unknown after an abrupt supervisor loss.
		return nil
	})
	if err != nil {
		return record, err
	}
	return store.Get(ctx, record.ID)
}

func schedulingPending(record Record, status unitStatus) (bool, error) {
	if status.LoadState != "not-found" || record.StartedAt != nil {
		return false, nil
	}
	bootID, err := currentBootID()
	if err != nil {
		return false, err
	}
	return schedulingPendingAt(record, status, bootID, time.Now()), nil
}

func schedulingPendingAt(record Record, status unitStatus, bootID string, now time.Time) bool {
	if status.LoadState != "not-found" || record.StartedAt != nil || (record.State != StateCreated && record.State != StateStarting) {
		return false
	}
	if record.BootID != "" && record.BootID != bootID {
		return false
	}
	age := now.Sub(record.CreatedAt)
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
			result = append(result, record)
		}
	}
	return result, nil
}

// Stop lets the runtime forward SIGTERM and clean up; systemd enforces a final
// whole-service kill boundary if the supervisor cannot complete its shutdown.
func Stop(ctx context.Context, ref string) (Record, error) {
	store, err := managementStore()
	if err != nil {
		return Record{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, managementStopLimit)
	defer cancel()
	record, err := store.Get(ctx, ref)
	if err != nil {
		return record, err
	}
	status, err := inspectUnit(ctx, record.ID)
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
			}
			return nil
		}); err != nil {
			return record, err
		}
	}
	if err := stopUnit(ctx, record.ID); err != nil {
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

func Remove(ctx context.Context, ref string) error {
	store, err := managementStore()
	if err != nil {
		return err
	}
	record, err := store.Get(ctx, ref)
	if err == nil {
		record, err = refreshRecord(ctx, store, record, false)
	}
	if err != nil {
		return err
	}
	if !record.Terminal() {
		return ErrNotTerminal
	}
	status, err := inspectUnit(ctx, record.ID)
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
		if _, err := systemdCommand(ctx, "systemctl", "reset-failed", unitName(record.ID)); err != nil {
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
	file, err := store.OpenLog(ctx, record.ID, false)
	if err != nil {
		return err
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		return err
	}
	if stat.Size() > MaxLogBytes {
		return errors.New("container log exceeds the retained size limit")
	}
	// Snapshot initial length so a continuously writing command cannot hold a
	// non-following logs request open. New bytes are streamed only with --follow.
	if err := copyInitialLog(file, stat.Size(), tail, out); err != nil || !follow {
		return err
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := io.Copy(out, file); err != nil {
			return err
		}
		record, err = store.Get(ctx, record.ID)
		if errors.Is(err, ErrNotFound) {
			return nil // The open inode remains readable after concurrent removal.
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
		if record.Terminal() {
			_, err := io.Copy(out, file) // Completion is written after the log closes.
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
