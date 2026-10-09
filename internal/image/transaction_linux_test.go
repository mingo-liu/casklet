//go:build linux

package image

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"
)

type delayedImage struct {
	v1.Image
	ctx     context.Context
	started chan struct{}
	release chan struct{}
}

func (img delayedImage) Layers() ([]v1.Layer, error) {
	layers, err := img.Image.Layers()
	if err == nil {
		layers[0] = delayedLayer{Layer: layers[0], ctx: img.ctx, started: img.started, release: img.release}
	}
	return layers, err
}

type delayedLayer struct {
	v1.Layer
	ctx     context.Context
	started chan struct{}
	release chan struct{}
}

func (layer delayedLayer) Compressed() (io.ReadCloser, error) {
	r, err := layer.Layer.Compressed()
	if err != nil {
		return nil, err
	}
	return &delayedReader{ReadCloser: r, ctx: layer.ctx, started: layer.started, release: layer.release}, nil
}

type delayedReader struct {
	io.ReadCloser
	ctx     context.Context
	started chan struct{}
	release chan struct{}
	once    sync.Once
	err     error
}

func (r *delayedReader) Read(p []byte) (int, error) {
	r.once.Do(func() {
		close(r.started)
		select {
		case <-r.release:
		case <-r.ctx.Done():
			r.err = r.ctx.Err()
		}
	})
	if r.err != nil {
		return 0, r.err
	}
	return r.ReadCloser.Read(p)
}

type pullResult struct {
	record Record
	err    error
}

func startDelayedPull(t *testing.T, store *Store, ctx context.Context, ref string, img v1.Image) (<-chan pullResult, func()) {
	t.Helper()
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	result := make(chan pullResult, 1)
	go func() {
		record, err := store.pullImage(ctx, ref, delayedImage{Image: img, ctx: ctx, started: started, release: release})
		result <- pullResult{record, err}
	}()
	select {
	case <-started:
	case outcome := <-result:
		t.Fatalf("pull finished before download: %v", outcome.err)
	case <-time.After(5 * time.Second):
		t.Fatal("pull did not begin download")
	}
	return result, unblock
}

func awaitPull(t *testing.T, result <-chan pullResult) pullResult {
	t.Helper()
	select {
	case outcome := <-result:
		return outcome
	case <-time.After(5 * time.Second):
		t.Fatal("pull did not finish")
		return pullResult{}
	}
}

func assertNoImageStaging(t *testing.T, store *Store) {
	t.Helper()
	entries, err := os.ReadDir(store.root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		for _, prefix := range []string{".import-", ".prepare-", ".transaction-", ".ref-stage-"} {
			if strings.HasPrefix(entry.Name(), prefix) {
				t.Errorf("staging left: %s", entry.Name())
			}
		}
	}
}

func TestSlowPullAllowsCachedReadersImportsAndUnrelatedPulls(t *testing.T) {
	store := testStore(t)
	cachedImage := fixtureImage(t, runtime.GOARCH, layerBytes(t, tarEntry{name: "app", body: "cached"}))
	cached, err := store.pullImage(context.Background(), "localhost/cached:v1", cachedImage)
	if err != nil {
		t.Fatal(err)
	}
	slowImage := fixtureImage(t, runtime.GOARCH, layerBytes(t, tarEntry{name: "app", body: "slow"}))
	result, unblock := startDelayedPull(t, store, context.Background(), "localhost/slow:v1", slowImage)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if records, err := store.List(ctx); err != nil || len(records) != 1 {
		t.Fatalf("list during download: %v %v", records, err)
	}
	if _, _, lease, err := store.Acquire(ctx, cached.ID); err != nil {
		t.Fatalf("acquire during download: %v", err)
	} else {
		lease.Close()
	}
	if imported, err := store.Import(ctx, testTemplate(t)); err != nil {
		t.Fatalf("import during download: %v", err)
	} else if err := store.Remove(ctx, imported.ID, unused); err != nil {
		t.Fatalf("remove during download: %v", err)
	}
	if other, err := store.pullImage(ctx, "localhost/other:v1", cachedImage); err != nil || other.ID != cached.ID {
		t.Fatalf("pull during download: %+v %v", other, err)
	}
	unblock()
	if outcome := awaitPull(t, result); outcome.err != nil {
		t.Fatal(outcome.err)
	}
	assertNoImageStaging(t, store)
}

func TestSameReferenceRefreshWaitIsCancelableAndOrdered(t *testing.T) {
	store := testStore(t)
	ref := "localhost/ordered:v1"
	first := fixtureImage(t, runtime.GOARCH, layerBytes(t, tarEntry{name: "app", body: "first"}))
	second := fixtureImage(t, runtime.GOARCH, layerBytes(t, tarEntry{name: "app", body: "second"}))
	result, unblock := startDelayedPull(t, store, context.Background(), ref, first)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if _, err := store.pullImage(ctx, ref, second); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("same-ref wait ignored deadline: %v", err)
	}
	if _, err := store.Resolve(context.Background(), ref); !errors.Is(err, ErrNotFound) {
		t.Fatalf("waiting refresh published prematurely: %v", err)
	}
	queued := make(chan pullResult, 1)
	go func() {
		record, err := store.pullImage(context.Background(), ref, second)
		queued <- pullResult{record, err}
	}()
	select {
	case outcome := <-queued:
		t.Fatalf("queued refresh completed before its predecessor: %+v %v", outcome.record, outcome.err)
	case <-time.After(40 * time.Millisecond):
	}
	unblock()
	old := awaitPull(t, result)
	if old.err != nil {
		t.Fatal(old.err)
	}
	update := awaitPull(t, queued)
	if update.err != nil || update.record.ID == old.record.ID {
		t.Fatalf("queued refresh failed: %+v %v", update.record, update.err)
	}
	if resolved, err := store.Resolve(context.Background(), ref); err != nil || resolved.ID != update.record.ID {
		t.Fatalf("final ref: %+v %v", resolved, err)
	}
	assertNoImageStaging(t, store)
}

func TestCanceledLivePullPreservesCacheAndRecoversAfterOwnerLoss(t *testing.T) {
	store := testStore(t)
	ref := "localhost/canceled:v1"
	good := fixtureImage(t, runtime.GOARCH, layerBytes(t, tarEntry{name: "app", body: "cached"}))
	cached, err := store.pullImage(context.Background(), ref, good)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	updated := fixtureImage(t, runtime.GOARCH, layerBytes(t, tarEntry{name: "app", body: "update"}))
	result, _ := startDelayedPull(t, store, ctx, ref, updated)
	// Import runs recovery while the download owns an external transaction lease.
	if _, err := store.Import(context.Background(), testTemplate(t)); err != nil {
		t.Fatal(err)
	}
	cancel()
	if outcome := awaitPull(t, result); !errors.Is(outcome.err, context.Canceled) {
		t.Fatalf("cancel: %v", outcome.err)
	}
	if resolved, err := store.Resolve(context.Background(), ref); err != nil || resolved.ID != cached.ID {
		t.Fatalf("cache changed on cancel: %+v %v", resolved, err)
	}
	assertNoImageStaging(t, store)

	stage, lease, transaction, err := store.beginImport(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Recursive cleanup may remove its internal lease before the external lock.
	if err := os.Remove(filepath.Join(stage, ".lease")); err != nil {
		t.Fatal(err)
	}
	lease.Close()
	if _, err := store.Import(context.Background(), testTemplate(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stage); err != nil {
		t.Fatalf("recovered a live partially cleaned tree: %v", err)
	}
	transaction.Close()
	if _, err := store.Import(context.Background(), testTemplate(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stage); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("abandoned tree remained: %v", err)
	}
	// A crash after identity rename leaves only the external transaction file.
	stage, lease, transaction, err = store.beginImport(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(stage); err != nil {
		t.Fatal(err)
	}
	lease.Close()
	if _, err := store.Import(context.Background(), testTemplate(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store.transactionPath(stage)); err != nil {
		t.Fatalf("live orphan lock removed: %v", err)
	}
	transaction.Close()
	if _, err := store.Import(context.Background(), testTemplate(t)); err != nil {
		t.Fatal(err)
	}
	assertNoImageStaging(t, store)
}

func TestConcurrentIdenticalPullsPublishOneImageAndAllReferences(t *testing.T) {
	store := testStore(t)
	img := fixtureImage(t, runtime.GOARCH, layerBytes(t, tarEntry{name: "app", body: "same"}))
	ready, release := make(chan struct{}, 4), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	results := make(chan pullResult, 4)
	for i := range 4 {
		ref := fmt.Sprintf("localhost/same:%d", i)
		go func() {
			ctx := WithProgress(context.Background(), func(event Progress) {
				if event.Stage == ProgressPublishing {
					ready <- struct{}{}
					<-release
				}
			})
			record, err := store.pullImage(ctx, ref, img)
			results <- pullResult{record, err}
		}()
	}
	for range 4 {
		select {
		case <-ready:
		case <-time.After(5 * time.Second):
			t.Fatal("independent pulls did not prepare concurrently")
		}
	}
	unblock()
	var first Record
	for range 4 {
		outcome := awaitPull(t, results)
		if outcome.err != nil {
			t.Fatal(outcome.err)
		}
		if first.ID == "" {
			first = outcome.record
		} else if !reflect.DeepEqual(first, outcome.record) {
			t.Fatal("concurrent publication replaced immutable metadata")
		}
	}
	records, err := store.List(context.Background())
	if err != nil || len(records) != 1 || len(records[0].References) != 4 {
		t.Fatalf("deduplication and refs: %+v %v", records, err)
	}
	assertNoImageStaging(t, store)
}

func TestPreparingImageSurvivesLegacyTransactionRecovery(t *testing.T) {
	store := testStore(t)
	stage, lease, transaction, err := store.beginImport(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	defer store.cleanupImport(stage, transaction)
	legacy := filepath.Join(store.root, ".import-legacy-abandoned")
	if err := os.Mkdir(legacy, 0700); err != nil {
		t.Fatal(err)
	}
	// Pinned older engines recover these historical prefixes without checking
	// transaction leases. New staging must not appear in their candidate set.
	entries, err := os.ReadDir(store.root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".import-") || strings.HasPrefix(entry.Name(), ".remove-") {
			if err := os.RemoveAll(filepath.Join(store.root, entry.Name())); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := os.Stat(legacy); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy recovery fixture did not reclaim historical staging: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stage, ".lease")); err != nil {
		t.Fatalf("legacy recovery removed active preparation: %v", err)
	}
}

func TestDeduplicatingPullRejectsCorruptPublishedImage(t *testing.T) {
	store := testStore(t)
	img := fixtureImage(t, runtime.GOARCH, layerBytes(t, tarEntry{name: "app", body: "original"}))
	firstRef, secondRef := "localhost/original:v1", "localhost/duplicate:v1"
	cached, err := store.pullImage(context.Background(), firstRef, img)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.path(cached.ID), "rootfs", "app"), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	// This reference has no cache: it downloads and prepares a valid duplicate,
	// then must reject rather than reuse or overwrite the corrupt existing ID.
	if _, err := store.pullImage(context.Background(), secondRef, img); err == nil {
		t.Fatal("deduplication accepted a corrupt published image")
	}
	if _, err := store.Resolve(context.Background(), secondRef); !errors.Is(err, ErrNotFound) {
		t.Fatalf("failed deduplication published a reference: %v", err)
	}
	if resolved, err := store.Resolve(context.Background(), firstRef); err != nil || resolved.ID != cached.ID {
		t.Fatalf("failed deduplication changed the original reference: %+v %v", resolved, err)
	}
	assertNoImageStaging(t, store)
}
