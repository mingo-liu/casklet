//go:build linux

package image

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// Cache management never holds the image metadata lock. Downloads take only
// digest locks; management tries those locks without waiting, preventing cycles.
func (store *Store) cacheLock(ctx context.Context) (*os.File, error) {
	if err := store.checkDirectory(store.root, true); err != nil {
		return nil, err
	}
	file, err := store.openFile(filepath.Join(store.root, ".cache-lock"), unix.O_RDWR|unix.O_CREAT)
	if err != nil {
		return nil, err
	}
	return waitFileLock(ctx, file, false)
}

type cachePolicy struct {
	MaxBytes int64 `json:"max_bytes"`
}

func (store *Store) cacheLimit() (int64, error) {
	file, err := store.openFile(filepath.Join(store.root, ".cache-policy"), unix.O_RDONLY)
	if errors.Is(err, os.ErrNotExist) {
		return DefaultCacheLimit, nil
	}
	if err != nil {
		return 0, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 129))
	if err != nil {
		return 0, err
	}
	var policy cachePolicy
	if len(data) > 128 || json.Unmarshal(data, &policy) != nil || policy.MaxBytes < 0 {
		return 0, errors.New("invalid download cache policy")
	}
	canonical, _ := json.Marshal(policy)
	if !bytes.Equal(bytes.TrimSpace(data), canonical) {
		return 0, errors.New("invalid download cache policy")
	}
	return policy.MaxBytes, nil
}

func (store *Store) writeCacheLimit(ctx context.Context, limit int64) error {
	entries, err := os.ReadDir(store.root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !strings.HasPrefix(entry.Name(), ".cache-policy-stage-") {
			continue
		}
		path := filepath.Join(store.root, entry.Name())
		file, err := store.openFile(path, unix.O_RDONLY)
		if err != nil {
			return err
		}
		file.Close()
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	file, err := os.CreateTemp(store.root, ".cache-policy-stage-")
	if err != nil {
		return err
	}
	defer file.Close()
	defer os.Remove(file.Name())
	if err := json.NewEncoder(file).Encode(cachePolicy{limit}); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), filepath.Join(store.root, ".cache-policy")); err != nil {
		return err
	}
	return syncDirectory(store.root)
}

func blobEntryDigest(name string) (string, error) {
	digest := strings.TrimPrefix(name, ".lock-")
	if strings.HasPrefix(name, ".stage-") {
		parts := strings.SplitN(strings.TrimPrefix(name, ".stage-"), "-", 2)
		if len(parts) != 2 || parts[1] == "" {
			return "", errors.New("invalid compressed layer staging name")
		}
		digest = parts[0]
	}
	if ValidateID("sha256:"+digest) != nil {
		return "", errors.New("invalid compressed layer cache entry")
	}
	return digest, nil
}

func (store *Store) tryBlobExclusive(dir, digest string) (*os.File, bool, error) {
	file, err := store.openFile(filepath.Join(dir, ".lock-"+digest), unix.O_RDWR|unix.O_CREAT)
	if err != nil {
		return nil, false, err
	}
	err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) {
		file.Close()
		return nil, false, nil
	}
	if err != nil {
		file.Close()
		return nil, false, err
	}
	return file, true, nil
}

func (store *Store) cacheEntry(dir, digest string) (CacheEntry, error) {
	file, err := store.openFile(filepath.Join(dir, digest), unix.O_RDONLY)
	if err != nil {
		return CacheEntry{}, err
	}
	defer file.Close()
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return CacheEntry{}, err
	}
	info, err := file.Stat()
	if err != nil {
		return CacheEntry{}, err
	}
	return CacheEntry{Digest: "sha256:" + digest, SizeBytes: info.Size(), AllocatedBytes: uint64(stat.Blocks) * 512, LastUsedAt: info.ModTime().UTC()}, nil
}

func (store *Store) cacheUsageLocked(ctx context.Context, limit int64) (CacheReport, error) {
	report := CacheReport{MaxBytes: limit, Entries: []CacheEntry{}}
	dir, err := store.blobDirectory(false)
	if errors.Is(err, os.ErrNotExist) {
		return report, nil
	}
	if err != nil {
		return report, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return report, err
	}
	for _, item := range entries {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		digest, err := blobEntryDigest(item.Name())
		if err != nil {
			return report, err
		}
		if item.Name() != digest {
			continue
		}
		lock, available, err := store.tryBlobExclusive(dir, digest)
		if err != nil {
			return report, err
		}
		entry, err := store.cacheEntry(dir, digest)
		if lock != nil {
			lock.Close()
		}
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return report, err
		}
		entry.InUse = !available
		if entry.SizeBytes > math.MaxInt64-report.SizeBytes {
			return report, errors.New("download cache size overflow")
		}
		report.SizeBytes += entry.SizeBytes
		report.AllocatedBytes += entry.AllocatedBytes
		if !entry.InUse {
			report.ReclaimableBytes += entry.AllocatedBytes
		}
		report.Entries = append(report.Entries, entry)
	}
	return report, nil
}

// CacheUsage reports retained blobs. In-use and reclaimable values are snapshots;
// prune and eviction recheck leases immediately before any mutation.
func (store *Store) CacheUsage(ctx context.Context) (CacheReport, error) {
	lock, err := store.cacheLock(ctx)
	if err != nil {
		return CacheReport{}, err
	}
	defer lock.Close()
	limit, err := store.cacheLimit()
	if err != nil {
		return CacheReport{}, err
	}
	return store.cacheUsageLocked(ctx, limit)
}

func (store *Store) SetCacheLimit(ctx context.Context, limit int64) (CacheReport, error) {
	if limit < 0 {
		return CacheReport{}, errors.New("download cache limit must be nonnegative")
	}
	lock, err := store.cacheLock(ctx)
	if err != nil {
		return CacheReport{}, err
	}
	defer lock.Close()
	// Validate existing policy artifacts before replacing their inode.
	if _, err := store.cacheLimit(); err != nil {
		return CacheReport{}, err
	}
	if err := store.writeCacheLimit(ctx, limit); err != nil {
		return CacheReport{}, err
	}
	if err := store.evictCacheLocked(ctx, limit); err != nil {
		return CacheReport{}, err
	}
	return store.cacheUsageLocked(ctx, limit)
}

func (store *Store) enforceCacheLimit(ctx context.Context) error {
	lock, err := store.cacheLock(ctx)
	if err != nil {
		return err
	}
	defer lock.Close()
	limit, err := store.cacheLimit()
	if err != nil {
		return err
	}
	return store.evictCacheLocked(ctx, limit)
}

func (store *Store) evictCacheLocked(ctx context.Context, limit int64) error {
	if limit == 0 {
		return nil
	}
	report, err := store.cacheUsageLocked(ctx, limit)
	if err != nil {
		return err
	}
	if report.SizeBytes <= limit {
		return nil
	}
	dir, err := store.blobDirectory(false)
	if err != nil {
		return err
	}
	sort.Slice(report.Entries, func(i, j int) bool {
		a, b := report.Entries[i], report.Entries[j]
		if a.LastUsedAt.Equal(b.LastUsedAt) {
			return a.Digest < b.Digest
		}
		return a.LastUsedAt.Before(b.LastUsedAt)
	})
	for _, entry := range report.Entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if report.SizeBytes <= limit {
			break
		}
		digest := strings.TrimPrefix(entry.Digest, "sha256:")
		lock, available, err := store.tryBlobExclusive(dir, digest)
		if err != nil {
			return err
		}
		if !available {
			continue
		}
		current, err := store.cacheEntry(dir, digest)
		if errors.Is(err, os.ErrNotExist) {
			report.SizeBytes -= entry.SizeBytes
			lock.Close()
			continue
		}
		if err != nil {
			lock.Close()
			return err
		}
		// A hit after the inventory is newer than this LRU candidate. Preserve it.
		if !current.LastUsedAt.Equal(entry.LastUsedAt) {
			lock.Close()
			continue
		}
		if err := ctx.Err(); err != nil {
			lock.Close()
			return err
		}
		err = os.Remove(filepath.Join(dir, digest))
		if err == nil {
			err = syncDirectory(dir)
		}
		lock.Close()
		if err != nil {
			return err
		}
		report.SizeBytes -= entry.SizeBytes
	}
	return nil
}

// PruneCache removes compressed data only. It never deletes image roots or
// references. Stable digest locks remain, including after preview and eviction.
func (store *Store) PruneCache(ctx context.Context, dryRun bool) (CachePruneResult, error) {
	result := CachePruneResult{DryRun: dryRun}
	lock, err := store.cacheLock(ctx)
	if err != nil {
		return result, err
	}
	defer lock.Close()
	dir, err := store.blobDirectory(false)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return result, err
	}
	seen := map[string]bool{}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		digest, err := blobEntryDigest(entry.Name())
		if err != nil {
			return result, err
		}
		if seen[digest] {
			continue
		}
		seen[digest] = true
		if err := store.pruneCacheDigest(ctx, dir, digest, dryRun, &result); err != nil {
			return result, err
		}
	}
	return result, nil
}

func (store *Store) pruneCacheDigest(ctx context.Context, dir, digest string, dryRun bool, result *CachePruneResult) error {
	lock, available, err := store.tryBlobExclusive(dir, digest)
	if err != nil {
		return err
	}
	if !available {
		return nil
	}
	defer lock.Close()
	entry, err := store.cacheEntry(dir, digest)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err == nil {
		if !dryRun {
			if err := os.Remove(filepath.Join(dir, digest)); err != nil {
				return err
			}
		}
		result.Blobs++
		result.ReclaimedBytes += entry.AllocatedBytes
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !strings.HasPrefix(entry.Name(), ".stage-"+digest+"-") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		file, err := store.openFile(path, unix.O_RDONLY)
		if err != nil {
			return err
		}
		var stat unix.Stat_t
		err = unix.Fstat(int(file.Fd()), &stat)
		file.Close()
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !dryRun {
			if err := os.Remove(path); err != nil {
				return err
			}
		}
		result.StagingFiles++
		result.ReclaimedBytes += uint64(stat.Blocks) * 512
	}
	if !dryRun {
		return syncDirectory(dir)
	}
	return nil
}

func touchCachedBlob(file *os.File) error {
	now := unix.NsecToTimeval(time.Now().UnixNano())
	if err := unix.Futimes(int(file.Fd()), []unix.Timeval{now, now}); err != nil {
		return fmt.Errorf("record download cache use: %w", err)
	}
	return nil
}
