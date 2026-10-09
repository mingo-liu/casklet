//go:build linux

package container

import (
	"context"
	"errors"
	"time"
)

// WaitHealthy waits for the observed execution's healthcheck to succeed. A zero
// timeout waits until success, lifecycle failure, or caller cancellation.
func WaitHealthy(ctx context.Context, ref string, timeout time.Duration) error {
	return withHealthWaitDeadline(ctx, timeout, func(ctx context.Context) error {
		if err := checkSystemd(); err != nil {
			return err
		}
		store, err := openStoreContext(ctx)
		if err != nil {
			return err
		}
		record, cfg, err := store.Snapshot(ctx, ref)
		if err != nil {
			return err
		}
		if !cfg.Healthcheck.Enabled() {
			return errors.New("container has no healthcheck; configure --health-cmd or use an image with HEALTHCHECK")
		}
		return waitHealthExecution(ctx, record, 0, func(ctx context.Context, id string) (Record, error) {
			latest, err := store.Get(ctx, id)
			if err != nil || latest.Generation != record.Generation {
				return latest, err
			}
			latest, err = refreshRecord(ctx, store, latest, false)
			if err != nil {
				return latest, err
			}
			// Refresh may observe a running unit before a concurrent stop or restart.
			return store.Get(ctx, id)
		})
	})
}
