//go:build linux

package container

import (
	"context"
	"errors"
	"os"

	"github.com/mingo-liu/casklet/internal/config"
)

// ListPage reads at most one page of matching records, without decoding IDs at
// or before the cursor or excluded IDs. Directory enumeration remains per page.
func (store *Store) ListPage(ctx context.Context, options RecordPageOptions) (RecordPage, error) {
	return store.listPage(ctx, options, RecordPageSize)
}

func (store *Store) listPage(ctx context.Context, options RecordPageOptions, limit int) (RecordPage, error) {
	if options.After != "" {
		if err := validateID(options.After); err != nil {
			return RecordPage{}, err
		}
	}
	if err := config.ValidateLabels(options.Labels); err != nil {
		return RecordPage{}, err
	}
	if len(options.ExcludeIDs) > RecordPageSize {
		return RecordPage{}, errors.New("too many excluded container IDs")
	}
	excluded := make(map[string]bool, len(options.ExcludeIDs))
	for _, id := range options.ExcludeIDs {
		if err := validateID(id); err != nil {
			return RecordPage{}, err
		}
		excluded[id] = true
	}
	if err := store.recoverTransactions(ctx); err != nil {
		return RecordPage{}, err
	}
	lock, err := store.lock(ctx, true)
	if err != nil {
		return RecordPage{}, err
	}
	defer lock.Close()
	entries, err := os.ReadDir(store.root)
	if err != nil {
		return RecordPage{}, err
	}
	page := RecordPage{Records: make([]Record, 0, limit)}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return RecordPage{}, err
		}
		id := entry.Name()
		if id <= options.After || !containerID.MatchString(id) || excluded[id] {
			continue
		}
		if len(page.Records) == limit {
			page.Next = page.Records[len(page.Records)-1].ID
			break
		}
		record, err := store.readRecord(id)
		if err != nil {
			return RecordPage{}, err
		}
		matches := true
		for key, value := range options.Labels {
			if actual, present := record.Labels[key]; !present || actual != value {
				matches = false
				break
			}
		}
		if matches {
			page.Records = append(page.Records, record)
		}
	}
	return page, ctx.Err()
}

// ListPage preserves List's supervisor recovery and effective health semantics,
// but refreshes only selected records, after releasing the metadata lock.
func ListPage(ctx context.Context, options RecordPageOptions) (RecordPage, error) {
	store, err := managementStore()
	if err != nil {
		return RecordPage{}, err
	}
	return refreshedPage(ctx, store, options, func(ctx context.Context, record Record) (Record, error) {
		return refreshRecord(ctx, store, record, false)
	})
}

func refreshedPage(ctx context.Context, store *Store, options RecordPageOptions, refresh func(context.Context, Record) (Record, error)) (RecordPage, error) {
	result := RecordPage{Records: make([]Record, 0, RecordPageSize)}
	for {
		page, err := store.listPage(ctx, options, RecordPageSize-len(result.Records))
		if err != nil {
			return RecordPage{}, err
		}
		for _, record := range page.Records {
			if err := ctx.Err(); err != nil {
				return RecordPage{}, err
			}
			record, err = refresh(ctx, record)
			if errors.Is(err, ErrNotFound) {
				continue // Removal after the page snapshot must not shorten a continuation.
			}
			if err != nil {
				return RecordPage{}, err
			}
			record.Health = record.effectiveHealth()
			result.Records = append(result.Records, record)
		}
		if page.Next == "" || len(result.Records) == RecordPageSize {
			result.Next = page.Next
			return result, ctx.Err()
		}
		options.After = page.Next
	}
}
