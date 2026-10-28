package project

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mingo-liu/casklet/internal/container"
)

// TaskFailure preserves a failed task's command status through deployment cleanup.
type TaskFailure struct {
	Service string
	Code    int
}

func (e *TaskFailure) Error() string {
	return fmt.Sprintf("task %q exited with status %d", e.Service, e.Code)
}

func taskResult(key, state string, code *int, reason string, cleanup []string) (bool, error) {
	if state == container.StateStopping {
		return false, fmt.Errorf("task %q is stopping", key)
	}
	if state != container.StateExited && state != container.StateFailed {
		return false, nil
	}
	if code == nil || *code < 0 || *code > 255 || state != container.StateExited || len(cleanup) > 0 {
		return false, fmt.Errorf("task %q did not complete successfully", key)
	}
	if *code != 0 {
		return false, &TaskFailure{key, *code}
	}
	if reason != "exited" {
		return false, fmt.Errorf("task %q did not finish normally", key)
	}
	return true, nil
}

func (runner *Runner) waitTask(ctx context.Context, name, key string, execution serviceExecution) error {
	if execution.ID == "" {
		return errors.New("task execution identity is missing")
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		records, err := runner.Backend.Status(ctx, name, []string{execution.ID})
		if err != nil {
			return err
		}
		if len(records) != 1 {
			return fmt.Errorf("task %q is missing or ambiguous", key)
		}
		record := records[0]
		if record.ID != execution.ID || record.Generation != execution.Generation || record.Project != name || record.Service != key {
			return fmt.Errorf("task %q execution changed", key)
		}
		done, err := taskResult(key, record.State, record.ExitCode, record.ExitReason, record.CleanupFailures)
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
