package container

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrWaitTimeout identifies a readiness deadline, rather than workload failure.
var ErrWaitTimeout = errors.New("timed out waiting for container health")

// waitHealthExecution observes one immutable execution without creating probes
// or holding lifecycle locks between samples. The reader also recovers stale
// supervisor state before reporting health.
func waitHealthExecution(ctx context.Context, initial Record, timeout time.Duration, read func(context.Context, string) (Record, error)) error {
	return withHealthWaitDeadline(ctx, timeout, func(waiting context.Context) error {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			if err := waiting.Err(); err != nil {
				return err
			}
			latest, err := read(waiting, initial.ID)
			if cancellation := waiting.Err(); cancellation != nil {
				return cancellation
			}
			if err != nil {
				return err
			}
			if latest.ID != initial.ID || latest.Generation != initial.Generation {
				return errors.New("container execution changed before becoming healthy")
			}
			if latest.Terminal() || latest.State == StateStopping || latest.Health != nil && latest.Health.Status == HealthStopped {
				return fmt.Errorf("container stopped before becoming healthy (state: %s)", latest.State)
			}
			if latest.State == StateRunning && latest.Health != nil && latest.Health.Status == HealthHealthy {
				return nil
			}
			select {
			case <-waiting.Done():
				return waiting.Err()
			case <-ticker.C:
			}
		}
	})
}

// withHealthWaitDeadline includes initial resolution and lock acquisition in the
// deadline, while keeping caller cancellation distinct from readiness timeout.
func withHealthWaitDeadline(ctx context.Context, timeout time.Duration, operation func(context.Context) error) error {
	if timeout < 0 {
		return errors.New("health wait timeout must be nonnegative")
	}
	waiting := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		waiting, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	contextError := func() error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if waiting.Err() != nil {
			return fmt.Errorf("%w after %s", ErrWaitTimeout, timeout)
		}
		return nil
	}
	if cancellation := contextError(); cancellation != nil {
		return cancellation
	}
	err := operation(waiting)
	if cancellation := contextError(); cancellation != nil {
		return cancellation
	}
	return err
}
