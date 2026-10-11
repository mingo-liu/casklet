package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"time"

	"github.com/mingo-liu/casklet/internal/container"
)

type statsOperations struct {
	list    func(context.Context, bool) ([]container.Record, error)
	inspect func(context.Context, string) (container.Inspection, error)
	sample  func(context.Context, []string, time.Duration) ([]container.Statistics, error)
}

func containerStatsOperations() statsOperations {
	return statsOperations{container.List, container.Inspect, container.StatsMany}
}

func executeStats(ctx context.Context, r Request, out io.Writer, ops statsOperations) error {
	// Snapshot selection once. Reusing a name or adding a matching label cannot
	// redirect a running monitor to another container.
	var ids []string
	names := map[string]string{}
	add := func(id, name string) {
		if _, found := names[id]; !found {
			ids = append(ids, id)
			names[id] = name
		}
	}
	if len(r.References) > 0 {
		for _, ref := range r.References {
			record, err := ops.inspect(ctx, ref)
			if err != nil {
				return err
			}
			add(record.ID, record.Name)
		}
	} else {
		records, err := ops.list(ctx, true)
		if err != nil {
			return err
		}
		for _, record := range container.FilterRecords(records, r.Filters) {
			add(record.ID, record.Name)
		}
		sort.Strings(ids)
	}
	single := len(r.References) == 1
	for {
		started := time.Now()
		values, err := ops.sample(ctx, ids, r.Interval)
		if err != nil {
			return err
		}
		for i := range values {
			if values[i].Name == "" {
				values[i].Name = names[values[i].ID]
			}
		}
		if single {
			if len(values) != 1 {
				return errors.New("invalid single-container statistics result")
			}
			err = writeStats(out, values[0], r.JSON)
		} else if r.JSON {
			if values == nil {
				values = []container.Statistics{}
			}
			err = json.NewEncoder(out).Encode(values)
		} else {
			err = writeStatsRows(out, values)
		}
		if err != nil || !r.Watch || len(ids) == 0 {
			return err
		}
		// Unavailable metrics return immediately; avoid spinning on stopped/removed
		// containers. Active sampling already consumed the refresh interval.
		delay := max(time.Duration(0), r.Interval-time.Since(started))
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
