//go:build linux

package image

import (
	"archive/tar"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func cacheFixture(t *testing.T, store *Store) ([]*cachedBlob, string) {
	t.Helper()
	img := fixtureImage(t, runtime.GOARCH, layerBytes(t, tarEntry{name: "a", body: "first"}), layerBytes(t, tarEntry{name: "b", body: "second"}), layerBytes(t, tarEntry{name: "c", body: "third"}))
	layers, err := img.Layers()
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := img.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	blobs, err := store.downloadBlobs(context.Background(), layers, manifest.Layers, "localhost/cache-policy:v1", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeBlobs(blobs) })
	dir, err := store.blobDirectory(false)
	if err != nil {
		t.Fatal(err)
	}
	return blobs, dir
}

func TestCachePolicyDefaultsAndPersistsAcrossStores(t *testing.T) {
	store := testStore(t)
	report, err := store.CacheUsage(context.Background())
	if err != nil || report.MaxBytes != DefaultCacheLimit || report.SizeBytes != 0 || report.Entries == nil {
		t.Fatalf("default: %+v %v", report, err)
	}
	for _, limit := range []int64{123456, 0, 1} {
		if _, err := store.SetCacheLimit(context.Background(), limit); err != nil {
			t.Fatal(err)
		}
		peer := cleanupPeer(t, store)
		if report, err := peer.CacheUsage(context.Background()); err != nil || report.MaxBytes != limit {
			t.Fatalf("persistent: %+v %v", report, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.SetCacheLimit(ctx, 9); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if report, err := store.CacheUsage(context.Background()); err != nil || report.MaxBytes != 1 {
		t.Fatalf("canceled policy changed: %+v %v", report, err)
	}
}

func TestCacheEvictionUsesVerifiedHitsAndPreservesActiveLeases(t *testing.T) {
	store := testStore(t)
	blobs, dir := cacheFixture(t, store)
	var total int64
	for i, blob := range blobs {
		total += blob.descriptor.Size
		old := time.Now().Add(time.Duration(i-3) * time.Hour)
		if err := os.Chtimes(filepath.Join(dir, blob.descriptor.Digest.Hex), old, old); err != nil {
			t.Fatal(err)
		}
	}
	report, err := store.SetCacheLimit(context.Background(), 1)
	if err != nil || report.SizeBytes != total || report.ReclaimableBytes != 0 {
		t.Fatalf("active leases: %+v %v", report, err)
	}
	for _, entry := range report.Entries {
		if !entry.InUse {
			t.Fatal("active blob reported idle")
		}
	}
	// A verified cache hit promotes the formerly oldest layer before eviction.
	hit, err := store.acquireBlob(context.Background(), dir, nil, blobs[0].descriptor, Progress{})
	if err != nil {
		t.Fatal(err)
	}
	hit.Close()
	limit := total - blobs[1].descriptor.Size
	closeBlobs(blobs)
	report, err = store.SetCacheLimit(context.Background(), limit)
	if err != nil || report.SizeBytes != limit || len(report.Entries) != 2 {
		t.Fatalf("LRU: %+v %v", report, err)
	}
	if _, err := os.Stat(filepath.Join(dir, blobs[1].descriptor.Digest.Hex)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("older idle blob retained: %v", err)
	}
	for _, i := range []int{0, 2} {
		if _, err := os.Stat(filepath.Join(dir, blobs[i].descriptor.Digest.Hex)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".lock-"+blobs[1].descriptor.Digest.Hex)); err != nil {
		t.Fatal("eviction removed stable lock", err)
	}
}

func TestPullCompletionEnforcesCapacityWithoutRemovingImages(t *testing.T) {
	for _, scenario := range []string{"success", "canceled", "extraction failure"} {
		t.Run(scenario, func(t *testing.T) {
			store := testStore(t)
			if _, err := store.SetCacheLimit(context.Background(), 1); err != nil {
				t.Fatal(err)
			}
			img := fixtureImage(t, runtime.GOARCH, layerBytes(t, tarEntry{name: "app", body: "still runnable"}))
			if scenario == "extraction failure" {
				img = fixtureImage(t, runtime.GOARCH, layerBytes(t, tarEntry{name: "unsafe", kind: tar.TypeFifo}))
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if scenario == "canceled" {
				ctx = WithProgress(ctx, func(event Progress) {
					if event.Stage == ProgressDownloaded {
						cancel()
					}
				})
			}
			record, err := store.pullImage(ctx, "localhost/limited:v1", img)
			if scenario == "canceled" {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("canceled pull: %v", err)
				}
			} else if scenario == "extraction failure" {
				if err == nil {
					t.Fatal("unsafe archive accepted")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				_, tree, lease, err := store.Acquire(context.Background(), record.ID)
				if err != nil {
					t.Fatal(err)
				}
				defer lease.Close()
				if data, err := os.ReadFile(filepath.Join(tree, "app")); err != nil || string(data) != "still runnable" {
					t.Fatalf("image lost after eviction: %q %v", data, err)
				}
			}
			report, err := store.CacheUsage(context.Background())
			if err != nil || report.SizeBytes != 0 {
				t.Fatalf("capacity not enforced: %+v %v", report, err)
			}
		})
	}
}

func TestIndependentCachePruneReportsPreviewAndPreservesImageLease(t *testing.T) {
	store := testStore(t)
	img := fixtureImage(t, runtime.GOARCH, layerBytes(t, tarEntry{name: "app", body: "retained image"}))
	record, err := store.pullImage(context.Background(), "localhost/prune-cache:v1", img)
	if err != nil {
		t.Fatal(err)
	}
	_, tree, lease, err := store.Acquire(context.Background(), record.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	manifest, _ := img.Manifest()
	dir, err := store.blobDirectory(false)
	if err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(dir, ".stage-"+manifest.Layers[0].Digest.Hex+"-abandoned")
	if err := os.WriteFile(stage, []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	preview, err := store.PruneCache(context.Background(), true)
	if err != nil || !preview.DryRun || preview.Blobs != 1 || preview.StagingFiles != 1 || preview.ReclaimedBytes == 0 {
		t.Fatalf("preview: %+v %v", preview, err)
	}
	if _, err := os.Stat(stage); err != nil {
		t.Fatal("preview changed staging", err)
	}
	result, err := store.PruneCache(context.Background(), false)
	if err != nil || result.Blobs != preview.Blobs || result.StagingFiles != preview.StagingFiles || result.ReclaimedBytes != preview.ReclaimedBytes {
		t.Fatalf("prune: %+v %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(tree, "app")); err != nil {
		t.Fatal("cache prune removed image", err)
	}
	if cached, err := store.Resolve(context.Background(), "localhost/prune-cache:v1"); err != nil || cached.ID != record.ID {
		t.Fatalf("reference lost: %+v %v", cached, err)
	}
}

func TestCacheManagementSkipsBlockedDownloadsAndDoesNotHoldMetadataLock(t *testing.T) {
	store := testStore(t)
	img, control := controlledImage(t, 1)
	t.Cleanup(func() { close(control.gates[0]) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	results := make(chan pullResult, 1)
	go func() {
		record, err := store.pullImage(ctx, "localhost/blocked-cache:v1", img)
		results <- pullResult{record, err}
	}()
	select {
	case <-control.started:
	case <-time.After(5 * time.Second):
		t.Fatal("download not started")
	}
	bounded, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if _, err := store.SetCacheLimit(bounded, 1); err != nil {
		t.Fatal(err)
	}
	if result, err := store.PruneCache(bounded, false); err != nil || result.Blobs != 0 || result.StagingFiles != 0 {
		t.Fatalf("active download pruned: %+v %v", result, err)
	}
	if _, err := cleanupPeer(t, store).Import(bounded, testTemplate(t)); err != nil {
		t.Fatal("unrelated import blocked", err)
	}
	cancel()
	if result := awaitPull(t, results); !errors.Is(result.err, context.Canceled) {
		t.Fatal(result.err)
	}
}

func TestCachePolicyRejectsUnsafeAndMalformedArtifacts(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "fifo", "public", "unknown field", "negative", "missing", "duplicate"} {
		t.Run(kind, func(t *testing.T) {
			store := testStore(t)
			path := filepath.Join(store.root, ".cache-policy")
			outside := filepath.Join(t.TempDir(), "keep")
			if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
				t.Fatal(err)
			}
			var err error
			switch kind {
			case "symlink":
				err = os.Symlink(outside, path)
			case "hardlink":
				err = os.Link(outside, path)
			case "fifo":
				err = unix.Mkfifo(path, 0600)
			default:
				data := `{"max_bytes":1}`
				mode := os.FileMode(0600)
				switch kind {
				case "public":
					mode = 0644
				case "unknown field":
					data = `{"max_bytes":1,"extra":2}`
				case "negative":
					data = `{"max_bytes":-1}`
				case "missing":
					data = `{}`
				case "duplicate":
					data = `{"max_bytes":1,"max_bytes":2}`
				}
				err = os.WriteFile(path, []byte(data), mode)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.CacheUsage(context.Background()); err == nil {
				t.Fatal("unsafe policy accepted")
			}
			if _, err := store.SetCacheLimit(context.Background(), 1); err == nil {
				t.Fatal("unsafe policy overwritten")
			}
			if data, err := os.ReadFile(outside); err != nil || string(data) != "outside" {
				t.Fatalf("outside data changed: %q %v", data, err)
			}
		})
	}
}

func TestCacheManagementLockWaitHonorsCancellation(t *testing.T) {
	store := testStore(t)
	lock, err := store.cacheLock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if _, err := cleanupPeer(t, store).PruneCache(ctx, false); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if _, err := store.List(context.Background()); err != nil {
		t.Fatal("cache lock held metadata lock", err)
	}
	if _, err := os.Stat(filepath.Join(store.root, ".cache-policy")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("lock wait created policy", err)
	}
}

func TestUnlimitedCacheRetainsBlobs(t *testing.T) {
	store := testStore(t)
	blobs, _ := cacheFixture(t, store)
	closeBlobs(blobs)
	report, err := store.SetCacheLimit(context.Background(), 0)
	if err != nil || len(report.Entries) != 3 {
		t.Fatalf("unlimited: %+v %v", report, err)
	}
	for _, entry := range report.Entries {
		if !strings.HasPrefix(entry.Digest, "sha256:") {
			t.Fatal(entry)
		}
	}
}
