//go:build darwin

package macos

import (
	"encoding/json"
	"github.com/mingo-liu/casklet/internal/image"
	"github.com/mingo-liu/casklet/internal/storage"
	"testing"
)

func TestDiskUsageThroughMacClient(t *testing.T) {
	var r storage.Report
	if err := json.Unmarshal([]byte(success(t, "system", "df", "--json")), &r); err != nil {
		t.Fatal(err)
	}
	if r.Filesystem.TotalBytes == 0 || len(r.Categories) != 6 {
		t.Fatalf("report %+v", r)
	}
	// User caches remain intact: actual pruning is verified in the isolated Linux VM.
	success(t, "image", "prune", "--dry-run")
}

func TestIndependentCacheStatusAndPreviewThroughMacClient(t *testing.T) {
	var before, after image.CacheReport
	if err := json.Unmarshal([]byte(success(t, "image", "cache", "ls", "--json")), &before); err != nil {
		t.Fatal(err)
	}
	var preview image.CachePruneResult
	if err := json.Unmarshal([]byte(success(t, "image", "cache", "prune", "--dry-run", "--json")), &preview); err != nil || !preview.DryRun {
		t.Fatalf("preview: %+v %v", preview, err)
	}
	if err := json.Unmarshal([]byte(success(t, "image", "cache", "ls", "--json")), &after); err != nil || before.SizeBytes != after.SizeBytes || len(before.Entries) != len(after.Entries) {
		t.Fatalf("preview changed user cache: %+v %+v %v", before, after, err)
	}
	success(t, "image", "cache", "ls")
	success(t, "image", "cache", "prune", "--dry-run")
}
