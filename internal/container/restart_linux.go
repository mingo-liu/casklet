//go:build linux

package container

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const restartUnit = "casklet-restarts.service"

// RestartManager runs only in its fixed systemd service. At most four container
// operations run concurrently; each remains serialized with user mutations.
func RestartManager() int {
	if os.Geteuid() != 0 {
		return 125
	}
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return 125
	}
	valid := false
	for _, line := range strings.Split(string(data), "\n") {
		if p, ok := strings.CutPrefix(line, "0::"); ok && filepath.Base(p) == restartUnit {
			valid = true
		}
	}
	if !valid {
		fmt.Fprintln(os.Stderr, "casklet: restart manager requires its dedicated systemd service")
		return 125
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	store, err := managementStore(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "casklet:", err)
		return 125
	}
	type result struct {
		id  string
		err error
	}
	completed := make(chan result, 4)
	active := map[string]bool{}
	lastError := map[string]time.Time{}
	var workers sync.WaitGroup
	defer workers.Wait()
	scanner := restartScanner{}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	report := func(id string, err error) {
		if err == nil {
			delete(lastError, id)
			return
		}
		if errors.Is(err, ErrBusy) || errors.Is(err, ErrNotFound) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		if time.Since(lastError[id]) >= 30*time.Second {
			fmt.Fprintf(os.Stderr, "casklet: automatic restart %s: %v\n", id, err)
			lastError[id] = time.Now()
		}
	}
	for {
		select {
		case <-ctx.Done():
			return 0
		case result := <-completed:
			delete(active, result.id)
			report(result.id, result.err)
		case <-ticker.C:
			if len(active) >= 4 {
				continue
			}
			scanCtx, scanCancel := context.WithTimeout(ctx, time.Second)
			ids, err := scanner.next(scanCtx, store, active, 4-len(active), report)
			scanCancel()
			if err != nil {
				report("storage", err)
				continue
			}
			for _, id := range ids {
				active[id] = true
				workers.Add(1)
				go func(id string) {
					defer workers.Done()
					completed <- result{id, reconcileRestart(ctx, store, id)}
				}(id)
			}
		}
	}
}

func reconcileRestart(ctx context.Context, store *Store, id string) error {
	lockCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	operation, err := store.AcquireOperation(lockCtx, id)
	cancel()
	if err != nil {
		return err
	}
	defer operation.Close()
	record, err := store.Get(ctx, id)
	if err != nil {
		return err
	}
	cfg, err := store.Config(ctx, id)
	if err != nil {
		return err
	}
	record, err = refreshRecord(ctx, store, record, false)
	if err != nil {
		return err
	}
	boot, err := currentBootID()
	if err != nil {
		return err
	}
	eligible, count := restartDecision(record, cfg, boot)
	if !eligible {
		return nil
	}
	if record.RestartAt == nil || record.RestartBootID != boot {
		at := time.Now().UTC().Add(restartDelay(count))
		return store.Update(ctx, id, func(r *Record) error {
			if r.Generation != record.Generation || !r.Terminal() {
				return ErrBusy
			}
			r.RestartCount = count
			r.RestartBootID = boot
			r.RestartAt = &at
			return nil
		})
	}
	if time.Now().Before(*record.RestartAt) {
		return nil
	}
	next, err := startStoppedWithPolicy(ctx, store, record, nil, true)
	if err == nil || errors.Is(err, ErrBusy) || ctx.Err() != nil {
		return err
	}
	// A preparation failure before BeginExecution must consume a retry too.
	// Once a generation starts, its completion already accounts for that retry.
	if next.Generation == record.Generation {
		updateCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		updateErr := store.Update(updateCtx, id, func(r *Record) error {
			if r.Generation != record.Generation || !r.Terminal() {
				return ErrBusy
			}
			if r.RestartCount < 1000000 {
				r.RestartCount++
			}
			at := time.Now().UTC().Add(restartDelay(r.RestartCount))
			r.RestartAt = &at
			r.Error = err.Error()
			return nil
		})
		return errors.Join(err, updateErr)
	}
	return err
}
