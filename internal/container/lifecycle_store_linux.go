//go:build linux

package container

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"golang.org/x/sys/unix"
)

// AcquireOperation serializes management mutations without blocking observers
// or supervisor updates. Callers pin the immutable ID before waiting.
func (store *Store) AcquireOperation(ctx context.Context, id string) (*os.File, error) {
	lock, err := store.lock(ctx, true)
	if err != nil {
		return nil, err
	}
	if _, err := store.readRecord(id); err != nil {
		lock.Close()
		return nil, err
	}
	file, err := store.openFile(filepath.Join(store.root, id, ".operation"), unix.O_RDWR, true)
	lock.Close()
	if err != nil {
		return nil, err
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			file.Close()
			return nil, err
		}
		err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return file, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EINTR) {
			file.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			file.Close()
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func receiptName(generation uint64) string {
	return "exit-" + strconv.FormatUint(generation, 10) + ".json"
}

func (store *Store) Completion(ctx context.Context, id string, generation uint64) (ExecutionResult, bool, error) {
	lock, err := store.lock(ctx, true)
	if err != nil {
		return ExecutionResult{}, false, err
	}
	defer lock.Close()
	record, err := store.readRecord(id)
	if err != nil {
		return ExecutionResult{}, false, err
	}
	var result ExecutionResult
	err = store.readJSON(filepath.Join(store.root, id, receiptName(generation)), maxRecordBytes, &result)
	if err == nil {
		if result.Generation != generation || result.ExitCode != nil && (*result.ExitCode < 0 || *result.ExitCode > 255) || validateCleanupFailures(result.CleanupFailures) != nil {
			return result, false, errors.New("invalid execution receipt")
		}
		return result, true, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return result, false, err
	}
	if record.Generation == generation && record.Terminal() {
		return executionResult(record), true, nil
	}
	if record.Generation > generation {
		return result, false, errors.New("execution completion receipt is missing")
	}
	return result, false, nil
}

func (store *Store) Complete(ctx context.Context, id string, generation uint64, update func(*Record)) error {
	lock, err := store.lock(ctx, false)
	if err != nil {
		return err
	}
	defer lock.Close()
	record, err := store.readRecord(id)
	if err != nil {
		return err
	}
	if record.Generation != generation {
		return errors.New("container execution changed")
	}
	if record.Terminal() {
		return nil
	}
	update(&record)
	if !record.Terminal() {
		return errors.New("completion requires a terminal state")
	}
	if err := validateRecord(record, id); err != nil {
		return err
	}
	path := filepath.Join(store.root, id)
	var saved ExecutionResult
	receiptErr := store.readJSON(filepath.Join(path, receiptName(generation)), maxRecordBytes, &saved)
	if receiptErr == nil {
		if saved.Generation != generation || saved.ExitCode != nil && (*saved.ExitCode < 0 || *saved.ExitCode > 255) || validateCleanupFailures(saved.CleanupFailures) != nil {
			return errors.New("invalid execution receipt identity")
		}
		record.StartedAt, record.FinishedAt, record.ExitCode = saved.StartedAt, saved.FinishedAt, saved.ExitCode
		record.CleanupFailures = append([]string(nil), saved.CleanupFailures...)
		record.State = StateFailed
		if saved.StartedAt != nil {
			record.State = StateExited
		}
		if err := validateRecord(record, id); err != nil {
			return err
		}
		return store.writeJSON(path, "state.json", record, maxRecordBytes)
	}
	if !errors.Is(receiptErr, os.ErrNotExist) {
		return receiptErr
	}
	if err := store.writeJSON(path, receiptName(generation), executionResult(record), maxRecordBytes); err != nil {
		return err
	}
	return store.writeJSON(path, "state.json", record, maxRecordBytes)
}

// BeginExecution requires both the operation lock and an inactive supervisor.
func (store *Store) BeginExecution(ctx context.Context, id string, generation uint64, automatic ...bool) (Record, error) {
	lock, err := store.lock(ctx, false)
	if err != nil {
		return Record{}, err
	}
	defer lock.Close()
	record, err := store.readRecord(id)
	if err != nil {
		return record, err
	}
	if record.Generation != generation || !record.Terminal() {
		return record, errors.New("container execution changed or is still active")
	}
	if generation == math.MaxUint64 {
		return record, errors.New("container execution counter is exhausted")
	}
	lease, err := store.openFile(filepath.Join(store.root, id, ".lease"), unix.O_RDWR, false)
	if err != nil {
		return record, err
	}
	defer lease.Close()
	if err := unix.Flock(int(lease.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return record, ErrBusy
	}
	previous := executionResult(record)
	if err := store.writeJSON(filepath.Join(store.root, id), receiptName(generation), previous, maxRecordBytes); err != nil {
		return record, err
	}
	boot, err := currentBootID()
	if err != nil {
		return record, err
	}
	now := time.Now().UTC()
	record.StoppedByUser = false
	record.StoppedBootID = ""
	record.RestartBootID = boot
	record.RestartAt = nil
	if len(automatic) > 0 && automatic[0] {
		if record.RestartCount < 1000000 {
			record.RestartCount++
		}
	} else {
		record.RestartCount = 0
	}
	record.Generation++
	record.LaunchAt = &now
	record.PreviousExit = &previous
	record.BootID = boot
	record.State = StateStarting
	record.LogLocking = true
	record.StartedAt, record.FinishedAt, record.ExitCode = nil, nil, nil
	record.Error, record.RunPath, record.Cgroup = "", "", ""
	record.CleanupFailures = nil
	record.StopTimeout = nil
	record.RetainRootFS = true
	if err := store.writeJSON(filepath.Join(store.root, id), "state.json", record, maxRecordBytes); err != nil {
		return record, err
	}
	return record, nil
}

func (store *Store) RootFS(ctx context.Context, id string) (string, error) {
	lock, err := store.lock(ctx, true)
	if err != nil {
		return "", err
	}
	defer lock.Close()
	if _, err := store.readRecord(id); err != nil {
		return "", err
	}
	return filepath.Join(store.root, id, "rootfs"), nil
}

func (store *Store) CleanupExecSocket(id string) error {
	path := filepath.Join(store.root, id, "exec.sock")
	var stat unix.Stat_t
	if err := unix.Lstat(path, &stat); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFSOCK || stat.Uid != store.owner || stat.Mode&0777 != 0600 {
		return fmt.Errorf("unsafe stale exec endpoint: %s", path)
	}
	return os.Remove(path)
}
