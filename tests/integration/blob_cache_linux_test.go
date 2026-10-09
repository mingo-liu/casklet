//go:build linux

package integration

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/mingo-liu/casklet/internal/image"
)

func TestImageSharedBlobCacheAndPruneThroughCLI(t *testing.T) {
	require(t)
	layer := func(filename, content string) v1.Layer {
		t.Helper()
		var data bytes.Buffer
		writer := tar.NewWriter(&data)
		if err := writer.WriteHeader(&tar.Header{Name: filename, Mode: 0644, Size: int64(len(content))}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		result, err := tarball.LayerFromReader(bytes.NewReader(data.Bytes()))
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	base := layer("shared", t.TempDir())
	baseDigest, err := base.Digest()
	if err != nil {
		t.Fatal(err)
	}
	var downloads atomic.Int32
	handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/blobs/"+baseDigest.String()) {
			downloads.Add(1)
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	refs := make([]string, 2)
	for i, value := range []string{"first", "second"} {
		img, err := mutate.AppendLayers(empty.Image, base, layer("order", value))
		if err != nil {
			t.Fatal(err)
		}
		cf, err := img.ConfigFile()
		if err != nil {
			t.Fatal(err)
		}
		cf.OS, cf.Architecture = "linux", runtime.GOARCH
		img, err = mutate.ConfigFile(img, cf)
		if err != nil {
			t.Fatal(err)
		}
		ref, err := name.ParseReference(strings.TrimPrefix(server.URL, "http://") + "/cache/app:" + value)
		if err != nil {
			t.Fatal(err)
		}
		if err := remote.Write(ref, img); err != nil {
			t.Fatal(err)
		}
		refs[i] = ref.Name()
	}
	downloads.Store(0)
	store, err := image.OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	var original image.CacheReport
	if err := json.Unmarshal([]byte(backgroundSuccess(t, "image", "cache", "ls", "--json")), &original); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { backgroundSuccess(t, "image", "cache", "limit", strconv.FormatInt(original.MaxBytes, 10)) })
	ids := []string{}
	for i, ref := range refs {
		code, out, stderr := start(t, "", "image", "pull", "--progress=plain", ref).wait(t)
		id := strings.TrimSpace(out)
		if code != 0 || image.ValidateID(id) != nil {
			t.Fatalf("pull: exit=%d stdout=%q stderr=%q", code, out, stderr)
		}
		t.Cleanup(func() { backgroundCLI(t, "image", "rm", id) })
		ids = append(ids, id)
		if i == 1 && !strings.Contains(stderr, "Already exists") {
			t.Fatalf("shared layer cache progress missing: %q", stderr)
		}
		_, tree, lease, err := store.Acquire(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(tree, "order"))
		lease.Close()
		if err != nil || string(data) != []string{"first", "second"}[i] {
			t.Fatalf("ordered extraction: %q %v", data, err)
		}
	}
	if downloads.Load() != 1 {
		t.Fatalf("shared base downloaded %d times", downloads.Load())
	}
	var preview image.CachePruneResult
	if err := json.Unmarshal([]byte(backgroundSuccess(t, "image", "cache", "prune", "--dry-run", "--json")), &preview); err != nil || preview.Blobs < 3 || preview.ReclaimedBytes == 0 {
		t.Fatalf("cache preview: %+v %v", preview, err)
	}
	var before, after image.CacheReport
	if err := json.Unmarshal([]byte(backgroundSuccess(t, "image", "cache", "ls", "--json")), &before); err != nil {
		t.Fatal(err)
	}
	backgroundSuccess(t, "image", "cache", "prune", "--dry-run")
	if err := json.Unmarshal([]byte(backgroundSuccess(t, "image", "cache", "ls", "--json")), &after); err != nil || before.SizeBytes != after.SizeBytes {
		t.Fatalf("preview changed cache: %+v %+v %v", before, after, err)
	}
	backgroundSuccess(t, "image", "cache", "prune", "--json")
	for _, id := range ids {
		_, tree, lease, err := store.Acquire(context.Background(), id)
		if err != nil {
			t.Fatal("independent prune removed image", err)
		}
		_, err = os.Stat(filepath.Join(tree, "order"))
		lease.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	// Removing compressed data does not change an unchanged cached image pull.
	backgroundSuccess(t, "image", "pull", refs[1])
	if downloads.Load() != 1 {
		t.Fatal("cached image pull downloaded again")
	}
	backgroundSuccess(t, "image", "prune", "--dry-run")
	backgroundSuccess(t, "image", "prune")
	backgroundSuccess(t, "image", "cache", "limit", "1")
	backgroundSuccess(t, "image", "pull", refs[1])
	if downloads.Load() != 2 {
		t.Fatalf("prune did not reclaim unused blob: downloads=%d", downloads.Load())
	}
	if err := json.Unmarshal([]byte(backgroundSuccess(t, "image", "cache", "ls", "--json")), &after); err != nil || after.MaxBytes != 1 || after.SizeBytes != 0 {
		t.Fatalf("CLI quota not enforced: %+v %v", after, err)
	}
	backgroundSuccess(t, "image", "cache", "limit", "0")
	if err := json.Unmarshal([]byte(backgroundSuccess(t, "image", "cache", "ls", "--json")), &after); err != nil || after.MaxBytes != 0 {
		t.Fatalf("unlimited policy: %+v %v", after, err)
	}
}
