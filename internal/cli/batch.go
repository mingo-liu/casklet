package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/mingo-liu/casklet/internal/container"
)

type batchOperations struct {
	list    func(context.Context, bool) ([]container.Record, error)
	resolve func(context.Context, string) (string, error)
	stop    func(context.Context, string, *time.Duration) (container.Record, error)
	remove  func(context.Context, string) error
}

func containerBatchOperations() batchOperations {
	return batchOperations{
		list: container.List,
		resolve: func(ctx context.Context, ref string) (string, error) {
			inspection, err := container.Inspect(ctx, ref)
			return inspection.ID, err
		},
		stop:   container.StopWithTimeout,
		remove: container.Remove,
	}
}

// executeBatch snapshots and pins full IDs before the first mutation. Selection
// does not confer removal permission: each operation rechecks under its lock.
func executeBatch(ctx context.Context, r Request, stdout io.Writer, ops batchOperations) error {
	var ids []string
	var failures []error
	seen := map[string]bool{}
	add := func(id string) {
		if !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	itemError := func(ref string, err error) error {
		return fmt.Errorf("%s %s: %w", r.Action, ref, operationError(Request{Action: r.Action, Reference: ref}, err))
	}
	if len(r.References) > 0 {
		for _, ref := range r.References {
			if err := ctx.Err(); err != nil {
				return errors.Join(append(failures, err)...)
			}
			id, err := ops.resolve(ctx, ref)
			if err != nil {
				failures = append(failures, itemError(ref, err))
				continue
			}
			add(id)
		}
	} else {
		records, err := ops.list(ctx, true)
		if err != nil {
			return err
		}
		for _, record := range container.FilterRecords(records, r.Filters) {
			add(record.ID)
		}
		sort.Strings(ids)
	}
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			failures = append(failures, err)
			break
		}
		var err error
		if r.Action == "stop" {
			_, err = ops.stop(ctx, id, r.StopTimeout)
		} else {
			err = ops.remove(ctx, id)
		}
		if err != nil {
			failures = append(failures, itemError(id, err))
			continue
		}
		if _, err := fmt.Fprintln(stdout, id); err != nil {
			failures = append(failures, err)
			break
		}
	}
	return errors.Join(failures...)
}
