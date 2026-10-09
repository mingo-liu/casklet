package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/mingo-liu/casklet/internal/image"
)

func TestCacheCommandsValidateSizesOptionsAndHelp(t *testing.T) {
	for _, args := range [][]string{{"image", "cache", "ls"}, {"image", "cache", "ls", "--json"}, {"image", "cache", "prune", "--dry-run", "--json"}} {
		if _, err := Parse(args); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	for value, want := range map[string]int64{"0": 0, "1": 1, "1k": 1 << 10, "2M": 2 << 20, "3g": 3 << 30} {
		request, err := Parse([]string{"image", "cache", "limit", "--json", value})
		if err != nil || request.Action != "image-cache-limit" || request.CacheLimit != want || !request.JSON {
			t.Fatalf("%s: %+v %v", value, request, err)
		}
	}
	for _, args := range [][]string{{"image", "cache"}, {"image", "cache", "unknown"}, {"image", "cache", "ls", "extra"}, {"image", "cache", "ls", "--dry-run"}, {"image", "cache", "limit"}, {"image", "cache", "limit", "1g", "extra"}, {"image", "cache", "limit", "--dry-run", "1g"}} {
		if _, err := Parse(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	for _, value := range []string{"-1", "1.5g", "0g", "9223372036854775807g", "1tb", "garbage"} {
		if _, err := Parse([]string{"image", "cache", "limit", value}); err == nil {
			t.Fatalf("accepted %s", value)
		}
	}
}

func TestCacheOutputDistinguishesPreviewUnlimitedAndEmptyData(t *testing.T) {
	var out bytes.Buffer
	if err := writeCacheReport(&out, image.CacheReport{Entries: []image.CacheEntry{}}, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "unlimited") || !strings.Contains(out.String(), "IDLE ALLOCATED BYTES") {
		t.Fatal(out.String())
	}
	out.Reset()
	if err := writeCachePrune(&out, image.CachePruneResult{DryRun: true, Blobs: 2, ReclaimedBytes: 4096}, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Would reclaim 4096 allocated bytes from 2 blobs") {
		t.Fatal(out.String())
	}
	out.Reset()
	if err := writeCachePrune(&out, image.CachePruneResult{Blobs: 1, StagingFiles: 2, ReclaimedBytes: 8192}, true); err != nil {
		t.Fatal(err)
	}
	if out.String() != "{\"dry_run\":false,\"blobs\":1,\"staging_files\":2,\"reclaimed_bytes\":8192}\n" {
		t.Fatal(out.String())
	}
}

func TestCacheSizeErrorsPointToFocusedHelp(t *testing.T) {
	_, err := Parse([]string{"image", "cache", "limit", "invalid"})
	if err == nil || !strings.Contains(err.Error(), "casklet image cache limit --help") {
		t.Fatal(err)
	}
}
