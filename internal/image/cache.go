package image

import "time"

// DefaultCacheLimit bounds retained compressed data, excluding active staging.
const DefaultCacheLimit int64 = 2 << 30

type CacheEntry struct {
	Digest         string    `json:"digest"`
	SizeBytes      int64     `json:"size_bytes"`
	AllocatedBytes uint64    `json:"allocated_bytes"`
	LastUsedAt     time.Time `json:"last_used_at"`
	InUse          bool      `json:"in_use"`
}

type CacheReport struct {
	MaxBytes         int64        `json:"max_bytes"`
	SizeBytes        int64        `json:"size_bytes"`
	AllocatedBytes   uint64       `json:"allocated_bytes"`
	ReclaimableBytes uint64       `json:"reclaimable_bytes"`
	Entries          []CacheEntry `json:"entries"`
}

type CachePruneResult struct {
	DryRun         bool   `json:"dry_run"`
	Blobs          int    `json:"blobs"`
	StagingFiles   int    `json:"staging_files"`
	ReclaimedBytes uint64 `json:"reclaimed_bytes"`
}
