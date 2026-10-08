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
	store, err := managementStore()
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
	cursor := 0
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
			listCtx, listCancel := context.WithTimeout(ctx, time.Second)
			records, err := store.List(listCtx)
			listCancel()
			if err != nil {
				report("storage", err)
				continue
			}
			for scanned := 0; scanned < len(records); scanned++ {
				if cursor >= len(records) {
					cursor = 0
				}
				record := records[cursor]
				cursor++
				if len(active) >= 4 {
					break
				}
				if active[record.ID] {
					continue
				}
				cfg, err := store.Config(ctx, record.ID)
				if err != nil {
					report(record.ID, err)
					continue
				}
				if cfg.RestartMode() == "no" {
					continue
				}
				active[record.ID] = true
				workers.Add(1)
				go func(id string) {
					defer workers.Done()
					err := reconcileRestart(ctx, store, id)
					completed <- result{id, err}
				}(record.ID)
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
