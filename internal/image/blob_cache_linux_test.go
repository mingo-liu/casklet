//go:build linux

package image

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"golang.org/x/sys/unix"
)

type blobHTTPCounts struct {
	mutex sync.Mutex
	gets  map[string]int
}

func (counts *blobHTTPCounts) count(digest string) int {
	counts.mutex.Lock()
	defer counts.mutex.Unlock()
	return counts.gets[digest]
}

func TestRegistryPullReusesSharedCompressedLayersAcrossImages(t *testing.T) {
	store := testStore(t)
	counts := &blobHTTPCounts{gets: map[string]int{}}
	handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodGet && strings.Contains(req.URL.Path, "/blobs/") {
			counts.mutex.Lock()
			counts.gets[filepath.Base(req.URL.Path)]++
			counts.mutex.Unlock()
		}
		handler.ServeHTTP(w, req)
	}))
	defer server.Close()
	base := layerBytes(t, tarEntry{name: "app", body: "base"}, tarEntry{name: "base", body: "shared"})
	first := fixtureImage(t, runtime.GOARCH, base, layerBytes(t, tarEntry{name: "app", body: "first"}))
	second := fixtureImage(t, runtime.GOARCH, base, layerBytes(t, tarEntry{name: "app", body: "second"}))
	firstManifest, err := first.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	secondManifest, err := second.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	refs := []name.Reference{}
	for i, img := range []v1.Image{first, second, second} {
		ref, err := name.ParseReference(strings.TrimPrefix(server.URL, "http://") + fmt.Sprintf("/test/cache:%d", i))
		if err != nil {
			t.Fatal(err)
		}
		if err := remote.Write(ref, img); err != nil {
			t.Fatal(err)
		}
		refs = append(refs, ref)
	}
	if _, err := store.Pull(context.Background(), refs[0].Name()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Pull(context.Background(), refs[1].Name()); err != nil {
		t.Fatal(err)
	}
	for _, d := range append(firstManifest.Layers, secondManifest.Layers[1:]...) {
		if got := counts.count(d.Digest.String()); got != 1 {
			t.Fatalf("blob %s GETs=%d, want one", d.Digest, got)
		}
	}
	var events []Progress
	ctx := WithProgress(context.Background(), func(event Progress) { events = append(events, event) })
	record, err := store.Pull(ctx, refs[2].Name())
	if err != nil {
		t.Fatal(err)
	}
	hits := 0
	for _, event := range events {
		if event.Stage == ProgressLayerCached {
			hits++
			if event.Current != 0 {
				t.Fatal("cache reuse counted network bytes")
			}
		}
		if event.Stage == ProgressDownloading || event.Stage == ProgressDownloaded {
			t.Fatalf("warm layer reported a download: %+v", event)
		}
	}
	if hits != 2 {
		t.Fatalf("cache events=%d, want two", hits)
	}
	for _, d := range secondManifest.Layers {
		if got := counts.count(d.Digest.String()); got != 1 {
			t.Fatalf("warm pull redownloaded %s: %d GETs", d.Digest, got)
		}
	}
	_, tree, lease, err := store.Acquire(context.Background(), record.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if content, err := os.ReadFile(filepath.Join(tree, "app")); err != nil || string(content) != "second" {
		t.Fatalf("layer order changed: %q %v", content, err)
	}
}

func TestConcurrentRegistryPullsDownloadEachDigestOnce(t *testing.T) {
	store := testStore(t)
	img := fixtureImage(t, runtime.GOARCH, layerBytes(t, tarEntry{name: "app", body: "shared"}))
	manifest, err := img.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	digest := manifest.Layers[0].Digest.String()
	counts := &blobHTTPCounts{gets: map[string]int{}}
	started, release := make(chan struct{}), make(chan struct{})
	var releaseOnce, startedOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/blobs/"+digest) {
			counts.mutex.Lock()
			counts.gets[digest]++
			counts.mutex.Unlock()
			startedOnce.Do(func() { close(started) })
			select {
			case <-release:
			case <-req.Context().Done():
				return
			}
		}
		handler.ServeHTTP(w, req)
	}))
	defer server.Close()
	defer unblock()
	refs := []name.Reference{}
	for i := range 2 {
		ref, err := name.ParseReference(strings.TrimPrefix(server.URL, "http://") + fmt.Sprintf("/test/concurrent:%d", i))
		if err != nil {
			t.Fatal(err)
		}
		if err := remote.Write(ref, img); err != nil {
			t.Fatal(err)
		}
		refs = append(refs, ref)
	}
	results := make(chan pullResult, 2)
	for _, ref := range refs {
		go func() {
			record, err := store.Pull(context.Background(), ref.Name())
			results <- pullResult{record, err}
		}()
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("download did not start")
	}
	unblock()
	var id string
	for range 2 {
		outcome := awaitPull(t, results)
		if outcome.err != nil {
			t.Fatal(outcome.err)
		}
		if id != "" && id != outcome.record.ID {
			t.Fatal("concurrent pull changed identity")
		}
		id = outcome.record.ID
	}
	if got := counts.count(digest); got != 1 {
		t.Fatalf("concurrent blob GETs=%d", got)
	}
	assertNoImageStaging(t, store)
}

type layerOverrideImage struct {
	v1.Image
	layers []v1.Layer
}

func (img layerOverrideImage) Layers() ([]v1.Layer, error) { return img.layers, nil }

type downloadController struct {
	started      chan int
	gates        []chan struct{}
	active       atomic.Int32
	maximum      atomic.Int32
	failureIndex int
	failure      error
}
type controlledLayer struct {
	v1.Layer
	control *downloadController
	index   int
}

func (layer controlledLayer) Compressed() (io.ReadCloser, error) {
	r, err := layer.Layer.Compressed()
	if err != nil {
		return nil, err
	}
	active := layer.control.active.Add(1)
	for maximum := layer.control.maximum.Load(); active > maximum; maximum = layer.control.maximum.Load() {
		if layer.control.maximum.CompareAndSwap(maximum, active) {
			break
		}
	}
	return &controlledReader{ReadCloser: r, control: layer.control, index: layer.index, closed: make(chan struct{})}, nil
}

type controlledReader struct {
	io.ReadCloser
	control   *downloadController
	index     int
	closed    chan struct{}
	readOnce  sync.Once
	closeOnce sync.Once
	err       error
}

func (r *controlledReader) Read(p []byte) (int, error) {
	r.readOnce.Do(func() {
		r.control.started <- r.index
		select {
		case <-r.control.gates[r.index]:
		case <-r.closed:
			r.err = context.Canceled
		}
		if r.index == r.control.failureIndex && r.control.failure != nil {
			r.err = r.control.failure
		}
	})
	if r.err != nil {
		return 0, r.err
	}
	return r.ReadCloser.Read(p)
}
func (r *controlledReader) Close() error {
	r.closeOnce.Do(func() { close(r.closed); r.ReadCloser.Close(); r.control.active.Add(-1) })
	return nil
}

func controlledImage(t *testing.T, count int) (v1.Image, *downloadController) {
	t.Helper()
	archives := make([][]byte, count)
	for i := range count {
		archives[i] = layerBytes(t, tarEntry{name: "app", body: fmt.Sprintf("layer-%d", i)})
	}
	img := fixtureImage(t, runtime.GOARCH, archives...)
	layers, err := img.Layers()
	if err != nil {
		t.Fatal(err)
	}
	control := &downloadController{started: make(chan int, count), gates: make([]chan struct{}, count), failureIndex: -1}
	for i := range count {
		control.gates[i] = make(chan struct{})
		layers[i] = controlledLayer{Layer: layers[i], control: control, index: i}
	}
	return layerOverrideImage{Image: img, layers: layers}, control
}

func TestBlobDownloadsAreBoundedAndLayerApplicationStaysOrdered(t *testing.T) {
	store := testStore(t)
	img, control := controlledImage(t, 7)
	var unblockOnce sync.Once
	unblock := func() {
		unblockOnce.Do(func() {
			for _, gate := range control.gates {
				close(gate)
			}
		})
	}
	t.Cleanup(unblock)
	results := make(chan pullResult, 1)
	var complete []int
	ctx := WithProgress(context.Background(), func(event Progress) {
		if event.Stage == ProgressLayerComplete {
			complete = append(complete, event.Index)
		}
	})
	go func() {
		record, err := store.pullImage(ctx, "localhost/bounded:v1", img)
		results <- pullResult{record, err}
	}()
	for range blobDownloadWorkers {
		select {
		case <-control.started:
		case <-time.After(5 * time.Second):
			t.Fatal("downloads did not run in parallel")
		}
	}
	select {
	case index := <-control.started:
		t.Fatalf("download %d exceeded worker limit", index)
	case <-time.After(40 * time.Millisecond):
	}
	unblock()
	outcome := awaitPull(t, results)
	if outcome.err != nil {
		t.Fatal(outcome.err)
	}
	if control.maximum.Load() != blobDownloadWorkers || control.active.Load() != 0 {
		t.Fatalf("download streams maximum=%d active=%d", control.maximum.Load(), control.active.Load())
	}
	for i, index := range complete {
		if index != i+1 {
			t.Fatalf("layers applied out of order: %v", complete)
		}
	}
	if len(complete) != 7 {
		t.Fatalf("completed layers=%v", complete)
	}
	_, tree, lease, err := store.Acquire(context.Background(), outcome.record.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if content, err := os.ReadFile(filepath.Join(tree, "app")); err != nil || string(content) != "layer-6" {
		t.Fatalf("last layer content=%q %v", content, err)
	}
}

func TestFailedOrCanceledBlobDownloadsCloseAndJoinAllWorkers(t *testing.T) {
	for _, mode := range []string{"canceled", "peer-failure"} {
		t.Run(mode, func(t *testing.T) {
			store := testStore(t)
			img, control := controlledImage(t, 6)
			failure := errors.New("injected compressed stream failure")
			if mode == "peer-failure" {
				control.failureIndex = 0
				control.failure = failure
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			results := make(chan pullResult, 1)
			go func() {
				record, err := store.pullImage(ctx, "localhost/cancel:v1", img)
				results <- pullResult{record, err}
			}()
			for range blobDownloadWorkers {
				select {
				case <-control.started:
				case <-time.After(5 * time.Second):
					t.Fatal("downloads did not start")
				}
			}
			if mode == "canceled" {
				cancel()
			} else {
				close(control.gates[0])
			}
			outcome := awaitPull(t, results)
			want := failure
			if mode == "canceled" {
				want = context.Canceled
			}
			if !errors.Is(outcome.err, want) {
				t.Fatalf("download failure=%v want=%v", outcome.err, want)
			}
			if control.active.Load() != 0 {
				t.Fatalf("returned with %d live streams", control.active.Load())
			}
			if records, err := store.List(context.Background()); err != nil || len(records) != 0 {
				t.Fatalf("failed image was published: %v %v", records, err)
			}
			assertNoImageStaging(t, store)
			assertNoBlobStages(t, store)
		})
	}
}

func assertNoBlobStages(t *testing.T, store *Store) {
	t.Helper()
	dir, err := store.blobDirectory(false)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".stage-") {
			t.Errorf("compressed staging left: %s", entry.Name())
		}
	}
}

func TestRepeatedManifestLayersReuseOneLeaseWithoutDeadlock(t *testing.T) {
	store := testStore(t)
	archive := layerBytes(t, tarEntry{name: "app", body: "repeat"})
	img := fixtureImage(t, runtime.GOARCH, archive, archive, archive)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := store.pullImage(ctx, "localhost/repeated:v1", img); err != nil {
		t.Fatal(err)
	}
	assertNoBlobStages(t, store)
}

func TestBlobPruneRespectsLiveLeasesDryRunAndStagingOwners(t *testing.T) {
	store := testStore(t)
	img := fixtureImage(t, runtime.GOARCH, layerBytes(t, tarEntry{name: "app", body: "prune"}))
	layers, err := img.Layers()
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := img.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	blobs, err := store.downloadBlobs(context.Background(), layers, manifest.Layers, "localhost/prune:v1", nil)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := store.blobDirectory(false)
	if err != nil {
		t.Fatal(err)
	}
	digest := manifest.Layers[0].Digest.Hex
	path := filepath.Join(dir, digest)
	if err := store.pruneBlobs(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("prune deleted leased blob: %v", err)
	}
	closeBlobs(blobs)
	if err := store.pruneBlobs(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("dry-run deleted blob: %v", err)
	}
	lease, err := store.blobLock(context.Background(), dir, digest, false)
	if err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(dir, ".stage-"+digest+"-abandoned")
	if err := os.WriteFile(stage, []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.pruneBlobs(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stage); err != nil {
		t.Fatalf("prune deleted active download staging: %v", err)
	}
	lease.Close()
	if err := store.pruneBlobs(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []string{path, stage} {
		if _, err := os.Stat(candidate); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("cache not pruned %s: %v", candidate, err)
		}
	}
	if lock, err := store.openFile(filepath.Join(dir, ".lock-"+digest), unix.O_RDONLY); err != nil {
		t.Fatalf("stable lock removed: %v", err)
	} else {
		lock.Close()
	}
}

type bytesLayer struct {
	v1.Layer
	data      []byte
	downloads *atomic.Int32
}

func (layer bytesLayer) Compressed() (io.ReadCloser, error) {
	layer.downloads.Add(1)
	return io.NopCloser(bytes.NewReader(layer.data)), nil
}

func TestCompressedManifestLimitsDigestAndActualSizeAreVerified(t *testing.T) {
	img := fixtureImage(t, runtime.GOARCH, layerBytes(t, tarEntry{name: "app", body: "checked"}))
	layers, err := img.Layers()
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := img.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	r, err := layers[0].Compressed()
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(r)
	r.Close()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"single-limit", "total-limit", "short", "long", "digest"} {
		t.Run(mode, func(t *testing.T) {
			store := testStore(t)
			var reads atomic.Int32
			d := manifest.Layers[0]
			input := append([]byte(nil), data...)
			switch mode {
			case "single-limit":
				d.Size = maxLayerBytes + 1
			case "short":
				d.Size++
			case "long":
				d.Size--
			case "digest":
				input[0] ^= 1
			}
			fake := bytesLayer{Layer: layers[0], data: input, downloads: &reads}
			descriptors, inputs := []v1.Descriptor{d}, []v1.Layer{fake}
			if mode == "total-limit" {
				d.Size = maxLayerBytes
				descriptors = make([]v1.Descriptor, 5)
				inputs = make([]v1.Layer, 5)
				for i := range 5 {
					descriptors[i] = d
					inputs[i] = fake
				}
			}
			if _, err := store.downloadBlobs(context.Background(), inputs, descriptors, "localhost/invalid:v1", nil); err == nil {
				t.Fatal("invalid compressed layer accepted")
			}
			if (mode == "single-limit" || mode == "total-limit") && reads.Load() != 0 {
				t.Fatal("manifest bounds rejected after starting download")
			}
			assertNoBlobStages(t, store)
		})
	}
}

func TestBlobCacheRejectsCorruptAndUnsafeArtifacts(t *testing.T) {
	img := fixtureImage(t, runtime.GOARCH, layerBytes(t, tarEntry{name: "app", body: "valid"}))
	layers, err := img.Layers()
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := img.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	d := manifest.Layers[0]
	for _, mode := range []string{"digest", "size", "symlink", "hardlink", "fifo", "public-file", "public-dir", "directory-link", "lock-link"} {
		t.Run(mode, func(t *testing.T) {
			store := testStore(t)
			dir, err := store.blobDirectory(true)
			if err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(t.TempDir(), "keep")
			if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, d.Digest.Hex)
			switch mode {
			case "digest":
				if err := os.WriteFile(path, bytes.Repeat([]byte{0}, int(d.Size)), 0600); err != nil {
					t.Fatal(err)
				}
			case "size":
				if err := os.WriteFile(path, []byte("wrong"), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(outside, path); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(outside, path); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				if err := unix.Mkfifo(path, 0600); err != nil {
					t.Fatal(err)
				}
			case "public-file":
				if err := os.WriteFile(path, []byte("wrong"), 0644); err != nil {
					t.Fatal(err)
				}
			case "public-dir":
				if err := os.Chmod(dir, 0755); err != nil {
					t.Fatal(err)
				}
			case "directory-link":
				if err := os.Remove(dir); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Dir(outside), dir); err != nil {
					t.Fatal(err)
				}
			case "lock-link":
				if err := os.Symlink(outside, filepath.Join(dir, ".lock-"+d.Digest.Hex)); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := store.downloadBlobs(context.Background(), layers, manifest.Layers, "localhost/unsafe:v1", nil); err == nil {
				t.Fatal("unsafe cache artifact accepted")
			}
			if content, err := os.ReadFile(outside); err != nil || string(content) != "outside" {
				t.Fatalf("outside file changed: %q %v", content, err)
			}
		})
	}
}

func TestBlobMissRecoversOnlyItsOwnAbandonedDownload(t *testing.T) {
	store := testStore(t)
	img := fixtureImage(t, runtime.GOARCH, layerBytes(t, tarEntry{name: "app", body: "stage"}))
	layers, err := img.Layers()
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := img.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := store.blobDirectory(true)
	if err != nil {
		t.Fatal(err)
	}
	own := filepath.Join(dir, ".stage-"+manifest.Layers[0].Digest.Hex+"-old")
	otherDigest := strings.Repeat("a", 64)
	other := filepath.Join(dir, ".stage-"+otherDigest+"-active")
	lease, err := store.blobLock(context.Background(), dir, otherDigest, false)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	for _, path := range []string{own, other} {
		if err := os.WriteFile(path, []byte("partial"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	blobs, err := store.downloadBlobs(context.Background(), layers, manifest.Layers, "localhost/recovery:v1", nil)
	if err != nil {
		t.Fatal(err)
	}
	closeBlobs(blobs)
	if _, err := os.Stat(own); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("own stale staging survived: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("different active digest recovered: %v", err)
	}
}

func TestHTTPDownloadFailureCancelsPeersWaitingForResponseHeaders(t *testing.T) {
	store := testStore(t)
	img := fixtureImage(t, runtime.GOARCH,
		layerBytes(t, tarEntry{name: "app", body: "one"}),
		layerBytes(t, tarEntry{name: "app", body: "two"}),
		layerBytes(t, tarEntry{name: "app", body: "three"}))
	manifest, err := img.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	indexes := map[string]int{}
	for i, d := range manifest.Layers {
		indexes[d.Digest.String()] = i
	}
	started, peersCanceled := make(chan int, 3), make(chan struct{}, 2)
	releaseFailure := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(releaseFailure) }) }
	t.Cleanup(unblock)
	handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if index, ok := indexes[filepath.Base(req.URL.Path)]; ok && req.Method == http.MethodGet && strings.Contains(req.URL.Path, "/blobs/") {
			started <- index
			if index == 0 {
				select {
				case <-releaseFailure:
				case <-req.Context().Done():
					return
				}
				http.Error(w, "injected blob failure", http.StatusNotFound)
				return
			}
			// These requests have not returned from Layer.Compressed: no
			// response headers and no ReadCloser exist yet to close locally.
			<-req.Context().Done()
			peersCanceled <- struct{}{}
			return
		}
		handler.ServeHTTP(w, req)
	}))
	defer server.Close()
	ref, err := name.ParseReference(strings.TrimPrefix(server.URL, "http://") + "/test/headers:v1")
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(ref, img); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	results := make(chan pullResult, 1)
	go func() { record, err := store.Pull(ctx, ref.Name()); results <- pullResult{record, err} }()
	for range 3 {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("HTTP layer requests did not run concurrently")
		}
	}
	unblock()
	outcome := awaitPull(t, results)
	if outcome.err == nil || errors.Is(outcome.err, context.DeadlineExceeded) || errors.Is(outcome.err, context.Canceled) {
		t.Fatalf("original blob failure lost or worker join timed out: %v", outcome.err)
	}
	for range 2 {
		select {
		case <-peersCanceled:
		case <-ctx.Done():
			t.Fatal("blocked response-header request survived peer failure")
		}
	}
	assertNoImageStaging(t, store)
	assertNoBlobStages(t, store)
}

func TestConcurrentPullsWithOppositeSharedLayerOrderComplete(t *testing.T) {
	store := testStore(t)
	a := layerBytes(t, tarEntry{name: "a", body: "shared-a"})
	b := layerBytes(t, tarEntry{name: "b", body: "shared-b"})
	archives := [][][]byte{
		{a, layerBytes(t, tarEntry{name: "first-c", body: "c"}), layerBytes(t, tarEntry{name: "first-d", body: "d"}), b},
		{b, layerBytes(t, tarEntry{name: "second-e", body: "e"}), layerBytes(t, tarEntry{name: "second-f", body: "f"}), a},
	}
	results := make(chan pullResult, 2)
	controls := make([]*downloadController, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for i, data := range archives {
		img := fixtureImage(t, runtime.GOARCH, data...)
		layers, err := img.Layers()
		if err != nil {
			t.Fatal(err)
		}
		control := &downloadController{started: make(chan int, 4), gates: make([]chan struct{}, 4), failureIndex: -1}
		for j := range 4 {
			control.gates[j] = make(chan struct{})
			layers[j] = controlledLayer{Layer: layers[j], control: control, index: j}
		}
		controls[i] = control
		go func() {
			record, err := store.pullImage(ctx, fmt.Sprintf("localhost/opposite:%d", i), layerOverrideImage{Image: img, layers: layers})
			results <- pullResult{record, err}
		}()
		// Pin the first three downloads before starting the other image, so
		// each pull owns a different shared blob while seeking the other's.
		for range 3 {
			select {
			case <-control.started:
			case <-ctx.Done():
				t.Fatal("initial downloads did not start")
			}
		}
	}
	for _, control := range controls {
		close(control.gates[1])
		close(control.gates[2])
	}
	// No fourth compressed stream can begin: both final digests are already
	// exclusively downloading in their peer's first worker.
	for _, control := range controls {
		select {
		case index := <-control.started:
			t.Fatalf("shared layer %d downloaded twice", index)
		case <-time.After(40 * time.Millisecond):
		}
	}
	for _, control := range controls {
		close(control.gates[0])
		close(control.gates[3])
	}
	for range 2 {
		outcome := awaitPull(t, results)
		if outcome.err != nil {
			t.Fatal(outcome.err)
		}
	}
	for _, control := range controls {
		if control.active.Load() != 0 {
			t.Fatal("opposite-order pull leaked streams")
		}
	}
	assertNoBlobStages(t, store)
}
