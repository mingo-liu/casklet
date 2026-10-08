//go:build linux

package container

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/mingo-liu/casklet/internal/config"
	containerruntime "github.com/mingo-liu/casklet/internal/runtime"
)

var errHealthInactive = errors.New("container health execution is no longer active")

func (server *execServer) startHealth() {
	cfg := server.resources.Config.Healthcheck
	if !cfg.Enabled() {
		return
	}
	server.workers.Add(1)
	go func() { defer server.workers.Done(); server.monitorHealth(cfg) }()
}

func (server *execServer) monitorHealth(cfg *config.HealthConfig) {
	record, err := server.healthRecord()
	if err != nil || record.Generation != server.generation || record.State != StateRunning || record.StartedAt == nil {
		return
	}
	started := *record.StartedAt
	health := Health{Status: HealthStarting, Checks: []HealthResult{}}
	null, err := os.OpenFile("/dev/null", os.O_RDWR, 0)
	if err != nil {
		return
	}
	defer null.Close()
	timer := time.NewTimer(health.delay(cfg, started, time.Now()))
	defer timer.Stop()
	for {
		select {
		case <-server.ctx.Done():
			return
		case <-timer.C:
		}
		record, err := server.healthRecord()
		if err != nil || record.Generation != server.generation || record.State != StateRunning {
			return
		}
		result := HealthResult{StartedAt: time.Now().UTC()}
		probe, cancel := context.WithTimeout(server.ctx, cfg.TimeoutValue())
		code, err := containerruntime.ExecuteProbe(probe, server.resources, cfg.Command(), null)
		result.TimedOut = errors.Is(probe.Err(), context.DeadlineExceeded)
		cancel()
		if server.ctx.Err() != nil {
			return
		}
		if result.TimedOut {
			code = 124
		}
		result.FinishedAt = time.Now().UTC()
		if result.FinishedAt.Before(result.StartedAt) {
			result.FinishedAt = result.StartedAt
		}
		result.ExitCode = code
		result.ExecutionFailed = err != nil
		health = health.next(cfg, started, result)
		save, cancel := context.WithTimeout(server.ctx, 5*time.Second)
		err = server.store.updateHealth(save, server.id, server.generation, health)
		cancel()
		if errors.Is(err, errHealthInactive) || server.ctx.Err() != nil {
			return
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "casklet: persist health result:", err)
		}
		timer.Reset(health.delay(cfg, started, time.Now()))
	}
}

func (s *Store) updateHealth(ctx context.Context, id string, generation uint64, health Health) error {
	return s.Update(ctx, id, func(record *Record) error {
		if record.Generation != generation || record.State != StateRunning {
			return errHealthInactive
		}
		record.Health = &health
		return nil
	})
}

// Metadata contention must not permanently stop a live container's monitor.
func (server *execServer) healthRecord() (Record, error) {
	for {
		lookup, cancel := context.WithTimeout(server.ctx, 5*time.Second)
		record, err := server.store.Get(lookup, server.id)
		cancel()
		if err == nil || errors.Is(err, ErrNotFound) || server.ctx.Err() != nil {
			return record, err
		}
		fmt.Fprintln(os.Stderr, "casklet: read health state:", err)
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-server.ctx.Done():
			timer.Stop()
			return Record{}, server.ctx.Err()
		case <-timer.C:
		}
	}
}
