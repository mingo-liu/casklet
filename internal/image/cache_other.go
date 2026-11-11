//go:build !linux

package image

import "context"

// CacheUsage reports that download cache management requires Linux.
func (*Store) CacheUsage(context.Context) (CacheReport, error) { return CacheReport{}, errUnsupported }

// SetCacheLimit reports that download cache management requires Linux.
func (*Store) SetCacheLimit(context.Context, int64) (CacheReport, error) {
	return CacheReport{}, errUnsupported
}

// PruneCache reports that download cache management requires Linux.
func (*Store) PruneCache(context.Context, bool) (CachePruneResult, error) {
	return CachePruneResult{}, errUnsupported
}
