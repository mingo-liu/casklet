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

// Stats retains the single-reference error contract.
func Stats(ctx context.Context, ref string, interval time.Duration) (Statistics, error) {
	if err := ValidateStatsInterval(interval); err != nil {
		return Statistics{}, err
	}
	store, err := managementStore()
	if err != nil {
		return Statistics{}, err
	}
	record, _, err := refreshedSnapshot(ctx, store, ref)
	if err != nil {
		return Statistics{}, err
	}
	results, err := sampleStatistics(ctx, store, []string{record.ID}, interval)
	if err != nil {
		return Statistics{}, err
	}
	return results[0], nil
}

// StatsMany samples pinned container IDs within one shared waiting window.
// A removed container produces unavailable metrics without hiding other rows.
func StatsMany(ctx context.Context, ids []string, interval time.Duration) ([]Statistics, error) {
	store, err := managementStore()
	if err != nil {
		return nil, err
	}
	return sampleStatistics(ctx, store, ids, interval)
}

type statsSample struct {
	result     Statistics
	generation uint64
	reader     *cgroup.MetricsReader
	first      cgroup.Metrics
}

func (sample *statsSample) unavailable(reason string) {
	sample.result.MemoryBytes, sample.result.CPUPercent, sample.result.CPUUsageUsec = nil, nil, nil
	sample.result.MemoryUnavailable, sample.result.CPUUnavailable = reason, reason
}

func sampleStatistics(ctx context.Context, store *Store, ids []string, interval time.Duration) ([]Statistics, error) {
	if err := ValidateStatsInterval(interval); err != nil {
		return nil, err
	}
	samples := make([]statsSample, len(ids))
	defer func() {
		for i := range samples {
			if samples[i].reader != nil {
				samples[i].reader.Close()
			}
		}
	}()
	active := false
	for i, id := range ids {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		sample := &samples[i]
		sample.result = Statistics{ID: id, State: "removed", SampledAt: time.Now().UTC(), Interval: "0s"}
		record, cfg, err := refreshedSnapshot(ctx, store, id)
		if errors.Is(err, ErrNotFound) {
			sample.unavailable("container was removed")
			continue
		}
		if err != nil {
			return nil, err
		}
		sample.generation = record.Generation
		sample.result.ID, sample.result.Name, sample.result.State = record.ID, record.Name, record.State
		sample.result.MemoryLimitBytes = cfg.Memory
		if record.Terminal() {
			sample.unavailable("container is completed; live metrics are not retained")
			continue
		}
		if record.Cgroup == "" {
			sample.unavailable("workload cgroup is not available")
			continue
		}
		if filepath.Clean(record.Cgroup) != record.Cgroup || !strings.HasPrefix(record.Cgroup, "/sys/fs/cgroup/") ||
			filepath.Base(filepath.Dir(record.Cgroup)) != unitName(record.ID, record.Generation) ||
			!workloadName.MatchString(filepath.Base(record.Cgroup)) {
			sample.unavailable("invalid workload cgroup identity")
			continue
		}
		sample.reader, err = cgroup.OpenMetrics(record.Cgroup)
		if err != nil {
			sample.unavailable("workload cgroup is not available")
			continue
		}
		sample.first = sample.reader.Sample()
		active = true
	}
	if active {
		timer := time.NewTimer(interval)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	results := make([]Statistics, len(samples))
	for i := range samples {
		sample := &samples[i]
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if sample.reader != nil {
			second := sample.reader.Sample()
			latest, err := store.Get(ctx, sample.result.ID)
			sample.result.SampledAt, sample.result.Interval = second.At.UTC(), second.At.Sub(sample.first.At).String()
			if errors.Is(err, ErrNotFound) {
				sample.result.State = "removed"
				sample.unavailable("container was removed")
			} else if err != nil {
				return nil, err
			} else {
				sample.result.State = latest.State
				if latest.Terminal() {
					sample.unavailable("container is completed; live metrics are not retained")
				} else if latest.Generation != sample.generation {
					sample.unavailable("container execution changed during sampling")
				} else {
					sample.result.MemoryBytes, sample.result.CPUUsageUsec = second.MemoryBytes, second.CPUUsageUsec
					sample.result.CPUPercent = cpuPercent(sample.first.CPUUsageUsec, second.CPUUsageUsec, second.At.Sub(sample.first.At))
					if sample.result.MemoryBytes == nil {
						sample.result.MemoryUnavailable = "memory counter is unavailable or invalid"
					}
					if sample.result.CPUPercent == nil {
						sample.result.CPUUnavailable = "CPU counters are unavailable, invalid, or reset"
					}
				}
			}
		}
		results[i] = sample.result
	}
	return results, nil
}
