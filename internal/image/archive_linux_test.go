//go:build linux

package image

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/mingo-liu/casklet/internal/rootfs"
)

func TestOCILoadSavePreservesIdentityOwnershipDefaultsAndReferences(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	record, err := store.Load(ctx, bytes.NewReader(archiveFixture(t)))
	if err != nil {
		t.Fatal(err)
	}
	if record.Config.Healthcheck.StartInterval == nil || *record.Config.Healthcheck.StartInterval != time.Second {
		t.Fatal("StartInterval lost")
	}
	if err := store.publishReferenceLocked(ctx, "example.com/application:fixture", record.ID); err != nil {
		t.Fatal(err)
	}
	_, tree, lease, err := store.Acquire(ctx, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(tree, "obsolete")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("whiteout: %v", err)
	}
	originalUID, originalGID := rootOwnership(t, tree, "value")
	symUID, symGID := rootOwnership(t, tree, "sym")
	lease.Close()
	var buf bytes.Buffer
	if err := store.Save(ctx, record.ID, &buf); err != nil {
		t.Fatal(err)
	}
	// A primary OCI reader accepts the exported standard index/config/layer.
	a, err := readOCIArchive(ctx, t.TempDir(), bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	lp, err := layout.FromPath(a.dir)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := lp.ImageIndex()
	if err != nil {
		t.Fatal(err)
	}
	im, err := idx.IndexManifest()
	if err != nil {
		t.Fatal(err)
	}
	thirdParty, err := idx.Image(im.Manifests[0].Digest)
	if err != nil {
		t.Fatal(err)
	}
	if cf, err := thirdParty.ConfigFile(); err != nil || cf.Architecture != runtime.GOARCH || cf.Config.User != "1000:1001" {
		t.Fatalf("export metadata: %+v %v", cf, err)
	}
	duplicate, err := store.Load(ctx, bytes.NewReader(buf.Bytes()))
	if err != nil || !reflect.DeepEqual(record, duplicate) {
		t.Fatalf("existing metadata changed: %+v %v", duplicate, err)
	}
	resolved, err := store.Resolve(ctx, "example.com/application:fixture")
	if err != nil || resolved.ID != record.ID {
		t.Fatalf("reference changed: %+v %v", resolved, err)
	}
	if err := store.Remove(ctx, record.ID, unused); err != nil {
		t.Fatal(err)
	}
	restored, err := store.Load(ctx, bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if restored.ID != record.ID || !reflect.DeepEqual(restored.Config, record.Config) || restored.ManifestDigest == record.ManifestDigest {
		t.Fatalf("roundtrip changed identity/config or retained manifest: %+v => %+v", record, restored)
	}
	_, tree, lease, err = store.Acquire(ctx, restored.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	uid, gid := rootOwnership(t, tree, "value")
	suid, sgid := rootOwnership(t, tree, "sym")
	if uid != originalUID || gid != originalGID || suid != symUID || sgid != symGID {
		t.Fatal("numeric ownership changed")
	}
}
func rootOwnership(t *testing.T, tree, name string) (uint32, uint32) {
	t.Helper()
	r, err := os.OpenRoot(tree)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	info, err := r.Lstat(name)
	if err != nil {
		t.Fatal(err)
	}
	uid, gid := rootfs.Ownership(info)
	return uid, gid
}
func TestOCILoadFailuresRollBackWithoutChangingPublishedImageOrCache(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	fixture := archiveFixture(t)
	record, err := store.Load(ctx, bytes.NewReader(fixture))
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.CacheUsage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, data := range [][]byte{fixture[:len(fixture)-1024], append(append([]byte(nil), fixture...), 1), []byte("bad")} {
		if _, err := store.Load(ctx, bytes.NewReader(data)); err == nil {
			t.Fatal("invalid archive published")
		}
	}
	records, err := store.List(ctx)
	if err != nil || len(records) != 1 || records[0].ID != record.ID {
		t.Fatalf("existing image changed: %+v %v", records, err)
	}
	after, err := store.CacheUsage(ctx)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("cache changed: %+v %+v %v", before, after, err)
	}
	entries, err := os.ReadDir(store.root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if len(e.Name()) >= 9 && e.Name()[:9] == ".prepare-" {
			t.Fatal("staging leaked")
		}
	}
}

type pausedArchiveWriter struct {
	ready  chan struct{}
	resume chan struct{}
	ctx    context.Context
	first  bool
}

func (w *pausedArchiveWriter) Write(p []byte) (int, error) {
	if !w.first {
		w.first = true
		close(w.ready)
		select {
		case <-w.resume:
		case <-w.ctx.Done():
			return 0, w.ctx.Err()
		}
	}
	return len(p), nil
}
func TestOCISaveLeaseDoesNotBlockMetadataAndReleasesOnCancel(t *testing.T) {
	store := testStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	record, err := store.Load(ctx, bytes.NewReader(archiveFixture(t)))
	if err != nil {
		t.Fatal(err)
	}
	writer := &pausedArchiveWriter{ready: make(chan struct{}), resume: make(chan struct{}), ctx: ctx}
	done := make(chan error, 1)
	go func() { done <- store.Save(ctx, record.ID, writer) }()
	select {
	case <-writer.ready:
	case <-time.After(5 * time.Second):
		t.Fatal("save not ready")
	}
	if _, err := store.List(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := store.Remove(context.Background(), record.ID, unused); !errors.Is(err, ErrInUse) {
		t.Fatalf("save lease not honored: %v", err)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	if err := store.Remove(context.Background(), record.ID, unused); err != nil {
		t.Fatal(err)
	}
}
func TestOCISaveRejectsDirectoryImport(t *testing.T) {
	store := testStore(t)
	record, err := store.Import(context.Background(), testTemplate(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(context.Background(), record.ID, io.Discard); err == nil {
		t.Fatal("export silently changed directory policy")
	}
}
