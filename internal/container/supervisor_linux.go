//go:build linux

package container

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	containerruntime "github.com/mingo-liu/casklet/internal/runtime"
)

// Supervisor is an internal entry point launched only by an immutable unit.
func Supervisor(id string, generations ...uint64) int {
	generation := uint64(0)
	if len(generations) != 0 {
		generation = generations[0]
	}
	if err := validateID(id); err != nil {
		fmt.Fprintln(os.Stderr, "casklet:", err)
		return 125
	}
	if err := checkSupervisorUnit(id, generation); err != nil {
		fmt.Fprintln(os.Stderr, "casklet:", err)
		return 125
	}
	store, err := OpenStore()
	if err != nil {
		fmt.Fprintln(os.Stderr, "casklet:", err)
		return 125
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	lease, err := store.AcquireLease(ctx, id)
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, "casklet:", err)
		return 125
	}
	defer lease.Close()
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	record, err := store.Get(ctx, id)
	cancel()
	if err != nil || record.Terminal() || record.Generation != generation {
		return 125
	}
	code, runErr, truncated := supervise(store, id, generation)
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := store.Complete(ctx, id, generation, func(record *Record) {
		record.CleanupFailures = containerruntime.CleanupStages(runErr)
		if record.Health != nil {
			record.Health.Status = HealthStopped
		}
		finished := time.Now().UTC()
		record.FinishedAt, record.ExitCode, record.LogTruncated = &finished, &code, truncated
		record.State = StateFailed
		if record.StartedAt != nil {
			record.State = StateExited
		}
		if runErr != nil {
			record.Error = runErr.Error()
		} else if record.State == StateFailed {
			record.Error = fmt.Sprintf("startup stopped before the command began (exit %d)", code)
		}
	}); err != nil {
		fmt.Fprintln(os.Stderr, "casklet: record completion:", err)
		return 125
	}
	return code
}

func supervise(store *Store, id string, generation uint64) (int, error, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := store.Update(ctx, id, func(record *Record) error {
		if record.Generation != generation || record.Terminal() {
			return errors.New("container execution changed before log capture")
		}
		record.LogLocking = true
		return nil
	}); err != nil {
		cancel()
		return 125, err, false
	}
	cfg, err := store.Config(ctx, id)
	cancel()
	if err != nil {
		return 125, err, false
	}
	root, err := store.RootFS(context.Background(), id)
	if err != nil {
		return 125, err, false
	}
	if err := store.cleanupRootFSStages(id); err != nil {
		return 125, err, false
	}
	record, err := store.Get(context.Background(), id)
	if err != nil {
		return 125, err, false
	}
	previouslyTruncated := record.LogTruncated

	input, err := os.Open("/dev/null")
	if err != nil {
		return 125, err, false
	}
	defer input.Close()
	reader, writer, err := os.Pipe()
	if err != nil {
		return 125, err, false
	}
	defer reader.Close()
	type captureResult struct {
		truncated bool
		err       error
	}
	completed := make(chan captureResult, 1)
	log := &rotatingLog{store: store, id: id, cfg: cfg, truncated: previouslyTruncated}
	go func() {
		err := captureLog(reader, log)
		completed <- captureResult{log.truncated, errors.Join(err, log.Sync())}
	}()

	executor := newExecServer(store, id, generation)
	observer := func(event containerruntime.Event) error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := store.Update(ctx, id, func(record *Record) error {
			if record.Terminal() || record.Generation != generation {
				return errors.New("container was finalized before startup")
			}
			record.RunPath = event.RunPath
			if event.Cgroup != "" {
				record.Cgroup = event.Cgroup
			}
			if event.Phase == "started" {
				started := time.Now().UTC()
				record.StartedAt = &started
				if cfg.Healthcheck.Enabled() {
					record.Health = &Health{Status: HealthStarting, Checks: []HealthResult{}}
				}
				if record.State != StateStopping {
					record.State = StateRunning
				}
			} else if record.State != StateStopping {
				record.State = StateStarting
			}
			return nil
		})
		if err == nil && event.Phase == "started" {
			executor.startHealth()
		}
		return err
	}
	code, runErr := containerruntime.RunManaged(cfg, input, writer, writer, observer, executor, root, func() time.Duration {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		record, err := store.Get(ctx, id)
		if err == nil && record.Generation == generation && record.StopTimeout != nil {
			return *record.StopTimeout
		}
		return cfg.StoppingTimeout()
	})
	if runErr != nil {
		fmt.Fprintln(writer, "casklet:", runErr)
	}
	closeErr := writer.Close()
	select {
	case result := <-completed:
		return code, errors.Join(runErr, closeErr, result.err), result.truncated
	case <-time.After(5 * time.Second):
		reader.Close()
		return code, errors.Join(runErr, errors.New("log capture did not finish after container cleanup")), true
	}
}

func checkSupervisorUnit(id string, generation ...uint64) error {
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if path, ok := strings.CutPrefix(line, "0::"); ok && filepath.Base(path) == unitName(id, generation...) {
			return nil
		}
	}
	return errors.New("internal supervisor requires its dedicated systemd service")
}
