package image

import (
	"context"
	"errors"
)

// Prune considers the initial image snapshot, then rechecks each candidate under
// the same lease/reference protocol as explicit removal. Newly pulled images are
// outside this operation. A partial result is retained if a later removal fails.
func (store *Store) Prune(ctx context.Context, dryRun bool, referenced ReferenceCheck) ([]Record, error) {
	records, err := store.List(ctx)
	if err != nil {
		return nil, err
	}
	result := []Record{}
	for _, r := range records {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		err := store.remove(ctx, r.ID, referenced, dryRun)
		if errors.Is(err, ErrInUse) || errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return result, err
		}
		result = append(result, r)
	}
	return result, store.pruneBlobs(ctx, dryRun)
}
