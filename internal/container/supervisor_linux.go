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

	containerruntime "github.com/mingo-liu/mini-docker/internal/runtime"
)

// Supervisor is an internal entry point launched only by an immutable unit.
func Supervisor(id string) int {
	if err := validateID(id); err != nil {
		fmt.Fprintln(os.Stderr, "mini-docker:", err)
		return 125
	}
	if err := checkSupervisorUnit(id); err != nil {
		fmt.Fprintln(os.Stderr, "mini-docker:", err)
		return 125
	}
	store, err := OpenStore()
	if err != nil {
		fmt.Fprintln(os.Stderr, "mini-docker:", err)
		return 125
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	lease, err := store.AcquireLease(ctx, id)
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, "mini-docker:", err)
		return 125
	}
	defer lease.Close()
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	record, err := store.Get(ctx, id)
	cancel()
	if err != nil || record.Terminal() {
		return 125
	}
	code, runErr, truncated := supervise(store, id)
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := store.Update(ctx, id, func(record *Record) error {
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
		return nil
	}); err != nil {
		fmt.Fprintln(os.Stderr, "mini-docker: record completion:", err)
		return 125
	}
	return code
}

func supervise(store *Store, id string) (int, error, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	cfg, err := store.Config(ctx, id)
	if err != nil {
		cancel()
		return 125, err, false
	}
	log, err := store.OpenLog(ctx, id, true)
	cancel()
	if err != nil {
		return 125, err, false
	}
	defer log.Close()
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
	go func() {
		truncated, err := captureLog(reader, log, MaxLogBytes)
		completed <- captureResult{truncated, errors.Join(err, log.Sync())}
	}()
	observer := func(event containerruntime.Event) error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return store.Update(ctx, id, func(record *Record) error {
			if record.Terminal() {
				return errors.New("container was finalized before startup")
			}
			record.RunPath = event.RunPath
			if event.Cgroup != "" {
				record.Cgroup = event.Cgroup
			}
			if event.Phase == "started" {
				started := time.Now().UTC()
				record.StartedAt = &started
				if record.State != StateStopping {
					record.State = StateRunning
				}
			} else if record.State != StateStopping {
				record.State = StateStarting
			}
			return nil
		})
	}
	code, runErr := containerruntime.RunWithObserver(cfg, input, writer, writer, observer)
	if runErr != nil {
		fmt.Fprintln(writer, "mini-docker:", runErr)
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

func checkSupervisorUnit(id string) error {
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if path, ok := strings.CutPrefix(line, "0::"); ok && filepath.Base(path) == unitName(id) {
			return nil
		}
	}
	return errors.New("internal supervisor requires its dedicated systemd service")
}
