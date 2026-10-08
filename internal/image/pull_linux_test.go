//go:build linux

package image

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
)

func fixtureImage(t *testing.T, arch string, layers ...[]byte) v1.Image {
	t.Helper()
	img := empty.Image
	for _, data := range layers {
		layer, err := tarball.LayerFromReader(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		img, err = mutate.AppendLayers(img, layer)
		if err != nil {
			t.Fatal(err)
		}
	}
	cf, err := img.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	cf.OS = "linux"
	cf.Architecture = arch
	cf.Config.Cmd = []string{"/app"}
	cf.Config.Env = []string{"COLOR=blue"}
	cf.Config.WorkingDir = "/work"
	img, err = mutate.ConfigFile(img, cf)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func TestRegistryPullSelectsPlatformCachesAndRefreshes(t *testing.T) {
	ctx := context.Background()
	store, err := newStoreAt(filepath.Join(t.TempDir(), "images"))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	defer server.Close()
	ref, err := name.ParseReference(strings.TrimPrefix(server.URL, "http://") + "/test/app:v1")
	if err != nil {
		t.Fatal(err)
	}
	native := fixtureImage(t, runtime.GOARCH, layerBytes(t, tarEntry{name: "app", body: "native"}, tarEntry{name: "remove", body: "old"}), layerBytes(t, tarEntry{name: ".wh.remove"}, tarEntry{name: "keep", body: "new"}))
	other := fixtureImage(t, map[string]string{"arm64": "amd64", "amd64": "arm64"}[runtime.GOARCH], layerBytes(t, tarEntry{name: "app", body: "other"}))
	index := mutate.AppendManifests(empty.Index, mutate.IndexAddendum{Add: other, Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: "linux", Architecture: map[string]string{"arm64": "amd64", "amd64": "arm64"}[runtime.GOARCH]}}}, mutate.IndexAddendum{Add: native, Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: "linux", Architecture: runtime.GOARCH}}})
	if err := remote.WriteIndex(ref, index); err != nil {
		t.Fatal(err)
	}
	first, err := store.Pull(ctx, ref.Name())
	if err != nil {
		t.Fatal(err)
	}
	if first.Config == nil || first.Config.Cmd[0] != "/app" || first.Architecture != runtime.GOARCH {
		t.Fatalf("metadata: %+v", first)
	}
	_, tree, lease, err := store.Acquire(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(tree, "app"))
	if err != nil || string(content) != "native" {
		t.Fatalf("wrong platform: %q %v", content, err)
	}
	if _, err := os.Stat(filepath.Join(tree, "remove")); !os.IsNotExist(err) {
		t.Fatal("whiteout was ignored")
	}
	if err := store.Remove(ctx, first.ID, func(context.Context, string) (bool, error) { return false, nil }); !errors.Is(err, ErrInUse) {
		t.Fatalf("leased deletion: %v", err)
	}
	lease.Close()
	duplicate, err := store.Pull(ctx, ref.Name())
	if err != nil || duplicate.ID != first.ID {
		t.Fatalf("duplicate pull: %+v %v", duplicate, err)
	}
	records, err := store.List(ctx)
	if err != nil || len(records) != 1 || len(records[0].References) != 1 || records[0].References[0] != ref.Name() {
		t.Fatalf("list refs: %+v %v", records, err)
	}
	updated := fixtureImage(t, runtime.GOARCH, layerBytes(t, tarEntry{name: "app", body: "updated"}))
	if err := remote.Write(ref, updated); err != nil {
		t.Fatal(err)
	}
	// Run resolution reuses its cached tag until an explicit pull refreshes it.
	cached, err := store.Resolve(ctx, ref.Name())
	if err != nil || cached.ID != first.ID {
		t.Fatalf("cache changed without pull: %+v %v", cached, err)
	}
	second, err := store.Pull(ctx, ref.Name())
	if err != nil || second.ID == first.ID {
		t.Fatalf("refresh: %+v %v", second, err)
	}
	server.Close()
	cached, err = store.Resolve(ctx, ref.Name())
	if err != nil || cached.ID != second.ID {
		t.Fatalf("offline cache: %+v %v", cached, err)
	}
	if err := store.Remove(ctx, second.ID, func(context.Context, string) (bool, error) { return false, nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Resolve(ctx, ref.Name()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removed cached image: %v", err)
	}
}

func TestPullRollbackAndConfigIntegrity(t *testing.T) {
	ctx := context.Background()
	store, err := newStoreAt(filepath.Join(t.TempDir(), "images"))
	if err != nil {
		t.Fatal(err)
	}
	ref := "index.docker.io/library/test:latest"
	good := fixtureImage(t, runtime.GOARCH, layerBytes(t, tarEntry{name: "app", body: "app"}))
	record, err := store.pullImage(ctx, ref, good)
	if err != nil {
		t.Fatal(err)
	}
	bad := fixtureImage(t, runtime.GOARCH, layerBytes(t, tarEntry{name: "../escape", body: "bad"}))
	if _, err := store.pullImage(ctx, ref, bad); err == nil {
		t.Fatal("unsafe pull accepted")
	}
	if cached, err := store.Resolve(ctx, ref); err != nil || cached.ID != record.ID {
		t.Fatalf("failed pull replaced cache: %+v %v", cached, err)
	}
	wrong := fixtureImage(t, "386", layerBytes(t, tarEntry{name: "app", body: "wrong"}))
	if _, err := store.pullImage(ctx, ref, wrong); err == nil {
		t.Fatal("wrong architecture accepted")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := store.pullImage(canceled, ref, good); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	entries, err := os.ReadDir(store.root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".import-") || strings.HasPrefix(entry.Name(), ".ref-stage-") {
			t.Fatalf("staging leaked: %s", entry.Name())
		}
	}
	// Defaults are hashed with rootfs ownership and content, so metadata tampering
	// cannot silently change the command of an existing identity.
	metadata := filepath.Join(store.path(record.ID), "image.json")
	data, err := os.ReadFile(metadata)
	if err != nil {
		t.Fatal(err)
	}
	data = bytes.Replace(data, []byte("/app"), []byte("/evil"), 1)
	if err := os.WriteFile(metadata, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, lease, err := store.Acquire(ctx, record.ID); err == nil {
		lease.Close()
		t.Fatal("tampered defaults accepted")
	}
}

func TestLayerDiffIDMismatchAndBoundedDownload(t *testing.T) {
	img := fixtureImage(t, runtime.GOARCH, layerBytes(t, tarEntry{name: "app", body: "app"}))
	layers, err := img.Layers()
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	archive, err := os.CreateTemp(t.TempDir(), "layer-")
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	if _, err := unpackLayer(context.Background(), root, archive, layers[0], v1.Hash{Algorithm: "sha256", Hex: strings.Repeat("0", 64)}, maxImageBytes); err == nil {
		t.Fatal("wrong DiffID accepted")
	}
	diff, err := layers[0].DiffID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := unpackLayer(context.Background(), root, archive, layers[0], diff, 1); err == nil {
		t.Fatal("oversized layer accepted")
	}
}
