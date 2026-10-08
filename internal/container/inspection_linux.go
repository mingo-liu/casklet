//go:build linux

package container

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/mingo-liu/casklet/internal/cgroup"
	"github.com/mingo-liu/casklet/internal/config"
)

// Snapshot reads state and configuration under one lock, so removal cannot split them.
func (store *Store) Snapshot(ctx context.Context, ref string) (Record, config.Config, error) {
	lock, err := store.lock(ctx, true)
	if err != nil {
		return Record{}, config.Config{}, err
	}
	defer lock.Close()
	record, err := store.resolveLocked(ref)
	if err != nil {
		return Record{}, config.Config{}, err
	}
	var cfg config.Config
	if err := store.readJSON(filepath.Join(store.root, record.ID, "config.json"), maxConfigBytes, &cfg); err != nil {
		return Record{}, config.Config{}, err
	}
	if err := cfg.ValidateExecution(); err != nil {
		// Validation errors can quote malformed environment assignments.
		return Record{}, config.Config{}, errors.New("invalid stored container configuration")
	}
	return record, cfg, nil
}

func refreshedSnapshot(ctx context.Context, store *Store, ref string) (Record, config.Config, error) {
	record, err := store.Get(ctx, ref)
	if err == nil {
		record, err = refreshRecord(ctx, store, record, false)
	}
	if err != nil {
		return Record{}, config.Config{}, err
	}
	// Resolve once: a concurrently reused name must never select a different container.
	return store.Snapshot(ctx, record.ID)
}

func Inspect(ctx context.Context, ref string) (Inspection, error) {
	store, err := managementStore()
	if err != nil {
		return Inspection{}, err
	}
	record, cfg, err := refreshedSnapshot(ctx, store, ref)
	if err != nil {
		return Inspection{}, err
	}
	return inspectRecord(record, cfg), nil
}

func Stats(ctx context.Context, ref string, interval time.Duration) (Statistics, error) {
	if err := ValidateStatsInterval(interval); err != nil {
		return Statistics{}, err
	}
	store, err := managementStore()
	if err != nil {
		return Statistics{}, err
	}
	record, cfg, err := refreshedSnapshot(ctx, store, ref)
	if err != nil {
		return Statistics{}, err
	}
	result := Statistics{ID: record.ID, Name: record.Name, State: record.State, SampledAt: time.Now().UTC(),
		Interval: "0s", MemoryLimitBytes: cfg.Memory}
	unavailable := func(reason string) (Statistics, error) {
		result.MemoryUnavailable, result.CPUUnavailable = reason, reason
		return result, nil
	}
	if record.Terminal() {
		return unavailable("container is completed; live metrics are not retained")
	}
	if record.Cgroup == "" {
		return unavailable("workload cgroup is not available")
	}
	// Refuse unrelated cgroups even if private metadata has been corrupted.
	if filepath.Clean(record.Cgroup) != record.Cgroup || !strings.HasPrefix(record.Cgroup, "/sys/fs/cgroup/") ||
		filepath.Base(filepath.Dir(record.Cgroup)) != unitName(record.ID, record.Generation) ||
		!workloadName.MatchString(filepath.Base(record.Cgroup)) {
		return unavailable("invalid workload cgroup identity")
	}
	reader, err := cgroup.OpenMetrics(record.Cgroup)
	if err != nil {
		return unavailable("workload cgroup is not available")
	}
	defer reader.Close()
	first := reader.Sample()
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return Statistics{}, ctx.Err()
	case <-timer.C:
	}
	second := reader.Sample()
	// Recheck by immutable ID after sampling, covering exit and concurrent removal.
	latest, err := store.Get(ctx, record.ID)
	if err != nil {
		return Statistics{}, err
	}
	result.State, result.SampledAt = latest.State, second.At.UTC()
	result.Interval = second.At.Sub(first.At).String()
	if latest.Terminal() || latest.Generation != record.Generation {
		return unavailable("container is completed; live metrics are not retained")
	}
	result.MemoryBytes, result.CPUUsageUsec = second.MemoryBytes, second.CPUUsageUsec
	result.CPUPercent = cpuPercent(first.CPUUsageUsec, second.CPUUsageUsec, second.At.Sub(first.At))
	if result.MemoryBytes == nil {
		result.MemoryUnavailable = "memory counter is unavailable or invalid"
	}
	if result.CPUPercent == nil {
		result.CPUUnavailable = "CPU counters are unavailable, invalid, or reset"
	}
	return result, nil
}
