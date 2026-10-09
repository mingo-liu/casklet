//go:build !linux

package image

import "context"

func (*Store) CacheUsage(context.Context) (CacheReport, error) { return CacheReport{}, errUnsupported }
func (*Store) SetCacheLimit(context.Context, int64) (CacheReport, error) {
	return CacheReport{}, errUnsupported
}
func (*Store) PruneCache(context.Context, bool) (CachePruneResult, error) {
	return CachePruneResult{}, errUnsupported
}
