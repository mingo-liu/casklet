//go:build linux

package container

import "context"

// The cursor bounds state decoding per pass; directory names are still enumerated.
// Policies are immutable for a container ID. Cache only this small value, and
// discard removed IDs after a complete traversal rather than retaining config.
type restartScanner struct {
	after    string
	pending  []Record
	policies map[string]string
	seen     map[string]bool
	end      bool
}

func (scan *restartScanner) next(ctx context.Context, store *Store, active map[string]bool, capacity int, report func(string, error)) ([]string, error) {
	if capacity <= 0 {
		return nil, nil
	}
	if scan.policies == nil {
		scan.policies = map[string]string{}
		scan.seen = map[string]bool{}
	}
	if len(scan.pending) == 0 {
		page, err := store.ListPage(ctx, RecordPageOptions{After: scan.after})
		if err != nil {
			return nil, err
		}
		scan.pending, scan.after, scan.end = page.Records, page.Next, page.Next == ""
	}
	ids := []string{}
	for len(scan.pending) > 0 && len(ids) < capacity {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		record := scan.pending[0]
		scan.pending = scan.pending[1:]
		scan.seen[record.ID] = true
		if active[record.ID] {
			continue
		}
		mode, found := scan.policies[record.ID]
		if !found {
			cfg, err := store.Config(ctx, record.ID)
			if err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				report(record.ID, err)
				continue
			}
			mode = cfg.RestartMode()
			scan.policies[record.ID] = mode
		}
		if mode != "no" {
			ids = append(ids, record.ID)
		}
	}
	if len(scan.pending) == 0 && scan.end {
		for id := range scan.policies {
			if !scan.seen[id] {
				delete(scan.policies, id)
			}
		}
		clear(scan.seen)
	}
	return ids, nil
}
