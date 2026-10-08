//go:build linux

package image

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

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

func TestLayerProgressReportsCompressedBytesExtractionAndFailure(t *testing.T) {
	img := fixtureImage(t, runtime.GOARCH, layerBytes(t, tarEntry{name: "app", body: strings.Repeat("application", 10000)}))
	manifest, err := img.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	layers, err := img.Layers()
	if err != nil {
		t.Fatal(err)
	}
	diff, err := layers[0].DiffID()
	if err != nil {
		t.Fatal(err)
	}
	for _, fail := range []string{"", "digest", "size", "canceled"} {
		t.Run(fail, func(t *testing.T) {
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
			var events []Progress
			ctx := WithProgress(context.Background(), func(event Progress) { events = append(events, event) })
			expected, remaining := diff, maxImageBytes
			switch fail {
			case "digest":
				expected.Hex = strings.Repeat("0", 64)
			case "size":
				remaining = 1
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			count, err := unpackLayer(ctx, root, archive, layers[0], expected, remaining, Progress{Layer: manifest.Layers[0].Digest.String(), Index: 1, Layers: 1, Total: manifest.Layers[0].Size})
			if (err != nil) != (fail != "") {
				t.Fatalf("unpack: %v", err)
			}
			if len(events) < 2 || events[0].Stage != ProgressDownloading {
				t.Fatalf("events: %+v", events)
			}
			last := events[len(events)-1]
			if fail != "" {
				if last.Stage != ProgressFailed && last.Stage != ProgressCanceled {
					t.Fatalf("missing failure: %+v", events)
				}
				for _, event := range events {
					if event.Stage == ProgressLayerComplete {
						t.Fatal("failure reported completion")
					}
				}
				return
			}
			if last.Stage != ProgressLayerComplete || last.Total != 2*count || last.Current != last.Total {
				t.Fatalf("extraction: %+v", events)
			}
			for _, stage := range []ProgressStage{ProgressDownloaded, ProgressVerifying, ProgressExtracting} {
				found := false
				for _, event := range events {
					if event.Stage == stage {
						found = true
						if stage == ProgressDownloaded && (event.Current != manifest.Layers[0].Size || event.Total != manifest.Layers[0].Size || event.Current >= count) {
							t.Fatalf("wrong compressed byte count: %+v, unpacked=%d", event, count)
						}
					}
				}
				if !found {
					t.Fatalf("missing %s: %+v", stage, events)
				}
			}
		})
	}
}

func TestPullCanceledByProgressPreservesCacheAndCleansStaging(t *testing.T) {
	store, err := newStoreAt(filepath.Join(t.TempDir(), "images"))
	if err != nil {
		t.Fatal(err)
	}
	ref := "index.docker.io/library/test:latest"
	good := fixtureImage(t, runtime.GOARCH, layerBytes(t, tarEntry{name: "app", body: "old"}))
	cached, err := store.pullImage(context.Background(), ref, good)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var events []Progress
	ctx = WithProgress(ctx, func(event Progress) {
		events = append(events, event)
		if event.Stage == ProgressExtracting {
			cancel()
		}
	})
	updated := fixtureImage(t, runtime.GOARCH, layerBytes(t, tarEntry{name: "app", body: "updated"}))
	if _, err := store.pullImage(ctx, ref, updated); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled pull: %v", err)
	}
	for _, event := range events {
		if event.Stage == ProgressReady || event.Stage == ProgressLayerComplete {
			t.Fatalf("canceled pull reported success: %+v", events)
		}
	}
	if len(events) == 0 || events[len(events)-1].Stage != ProgressCanceled {
		t.Fatalf("cancellation progress: %+v", events)
	}
	resolved, err := store.Resolve(context.Background(), ref)
	if err != nil || resolved.ID != cached.ID {
		t.Fatalf("cache after cancellation: %+v %v", resolved, err)
	}
	entries, err := os.ReadDir(store.root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".import-") {
			t.Fatalf("canceled staging leaked: %s", entry.Name())
		}
	}
}

func TestPullPreservesHealthcheckDefaults(t *testing.T) {
	store, err := newStoreAt(filepath.Join(t.TempDir(), "images"))
	if err != nil {
		t.Fatal(err)
	}
	img := fixtureImage(t, runtime.GOARCH, layerBytes(t, tarEntry{name: "app", body: "native"}))
	cf, err := img.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	cf.Config.Healthcheck = &v1.HealthConfig{Test: []string{"CMD", "/app"}, Interval: 2 * time.Second, Timeout: time.Second, StartPeriod: 4 * time.Second, Retries: 2}
	cf.Config.Shell = []string{"/bin/custom", "-c"}
	img, err = mutate.ConfigFile(img, cf)
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.pullImage(context.Background(), "localhost/health:test", img)
	if err != nil {
		t.Fatal(err)
	}
	if record.Config.Healthcheck == nil || record.Config.Healthcheck.IntervalValue() != 2*time.Second || record.Config.Healthcheck.Test[1] != "/app" {
		t.Fatalf("pulled: %+v", record)
	}
	read, err := store.Resolve(context.Background(), record.ID)
	if err != nil || read.Config.Healthcheck.RetriesValue() != 2 {
		t.Fatalf("persisted: %+v %v", read, err)
	}
}

// Model the previous engine's decoded view of the same registry manifest.
type legacyHealthImage struct {
	v1.Image
	cf *v1.ConfigFile
}

func (i legacyHealthImage) ConfigFile() (*v1.ConfigFile, error) { return i.cf, nil }
func (i legacyHealthImage) RawConfigFile() ([]byte, error)      { return json.Marshal(i.cf) }

func TestPullRefreshesLegacyHealthMetadataWithoutManifestChange(t *testing.T) {
	store, err := newStoreAt(filepath.Join(t.TempDir(), "images"))
	if err != nil {
		t.Fatal(err)
	}
	img := fixtureImage(t, runtime.GOARCH, layerBytes(t, tarEntry{name: "app", body: "same"}))
	cf, err := img.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	cf.Config.Healthcheck = &v1.HealthConfig{Test: []string{"CMD", "/app"}, Interval: time.Second}
	img, err = mutate.ConfigFile(img, cf)
	if err != nil {
		t.Fatal(err)
	}
	legacy := cf.DeepCopy()
	legacy.Config.Healthcheck = nil
	old, err := store.pullImage(context.Background(), "localhost/upgrade:test", legacyHealthImage{Image: img, cf: legacy})
	if err != nil {
		t.Fatal(err)
	}
	current, err := store.pullImage(context.Background(), "localhost/upgrade:test", img)
	if err != nil {
		t.Fatal(err)
	}
	if old.ManifestDigest != current.ManifestDigest || old.ID == current.ID || current.Config.Healthcheck == nil {
		t.Fatalf("did not refresh metadata: old=%+v new=%+v", old, current)
	}
	pinned, err := store.Resolve(context.Background(), old.ID)
	if err != nil || pinned.Config.Healthcheck != nil {
		t.Fatal("modified old immutable image")
	}
	latest, err := store.Resolve(context.Background(), "localhost/upgrade:test")
	if err != nil || latest.ID != current.ID {
		t.Fatalf("reference not refreshed: %+v %v", latest, err)
	}
	again, err := store.pullImage(context.Background(), "localhost/upgrade:test", img)
	if err != nil || again.ID != current.ID || !again.CreatedAt.Equal(current.CreatedAt) {
		t.Fatalf("unchanged image republished: %+v %v", again, err)
	}
}
