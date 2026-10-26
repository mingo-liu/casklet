//go:build linux

package container

import (
	"context"
	"errors"
)

// ListIDs samples at most 64 pinned full IDs without resolving names or reading
// unrelated records. Missing IDs are omitted so callers can fence their selection.
func ListIDs(ctx context.Context, ids []string) ([]Record, error) {
	store, err := managementStore(ctx)
	if err != nil {
		return nil, err
	}
	return listIDs(ctx, store, ids, func(ctx context.Context, record Record) (Record, error) {
		return refreshRecord(ctx, store, record, false)
	})
}

func listIDs(ctx context.Context, store *Store, ids []string, refresh func(context.Context, Record) (Record, error)) ([]Record, error) {
	if len(ids) == 0 || len(ids) > RecordPageSize {
		return nil, errors.New("container status requires 1 to 64 full IDs")
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if err := validateID(id); err != nil {
			return nil, err
		}
		if seen[id] {
			return nil, errors.New("duplicate container status ID")
		}
		seen[id] = true
	}
	result := make([]Record, 0, len(ids))
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		record, err := store.Get(ctx, id)
		if err == nil {
			record, err = refresh(ctx, record)
		}
		if errors.Is(err, ErrNotFound) {
			continue // Never follow a concurrently reused name or shortened ID.
		}
		if err != nil {
			return nil, err
		}
		record.Health = record.effectiveHealth()
		result = append(result, record)
	}
	return result, ctx.Err()
}
