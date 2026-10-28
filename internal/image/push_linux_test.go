//go:build linux

package image

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

func TestTagReplacementAndPushRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	first, err := store.Load(ctx, bytes.NewReader(archiveFixture(t)))
	if err != nil {
		t.Fatal(err)
	}
	imported, err := store.Import(ctx, testTemplate(t))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	defer server.Close()
	target := strings.TrimPrefix(server.URL, "http://") + "/app:v1"
	if err := store.Tag(ctx, first.ID, target); err != nil {
		t.Fatal(err)
	}
	if err := store.Tag(ctx, imported.ID, target); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Resolve(ctx, target); err != nil || got.ID != imported.ID {
		t.Fatalf("replacement: %+v %v", got, err)
	}
	for _, record := range []Record{first, imported} {
		digest, err := store.Push(ctx, record.ID, target)
		if err != nil {
			t.Fatal(err)
		}
		ref, _ := name.ParseReference(target)
		img, err := remote.Image(ref)
		if err != nil {
			t.Fatal(err)
		}
		hash, err := img.Digest()
		if err != nil || hash.String() != digest {
			t.Fatalf("digest: %s %v", hash, err)
		}
		destination := testStore(t)
		pulled, err := destination.Pull(ctx, target)
		if err != nil {
			t.Fatal(err)
		}
		if record.Config != nil && !sameLaunchConfig(record.Config, pulled.Config) {
			t.Fatal("push lost launch defaults")
		}
		if record.Config != nil && pulled.ID != record.ID {
			t.Fatal("OCI identity changed")
		}
	}
	if got, err := store.Resolve(ctx, target); err != nil || got.ID != imported.ID {
		t.Fatal("push changed local mapping")
	}
	if err := store.Tag(ctx, first.ID, "app@"+first.ID); err == nil {
		t.Fatal("accepted digest tag")
	}
	if err := store.Tag(ctx, "sha256:"+strings.Repeat("f", 64), target); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := store.Push(canceled, first.ID, target); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := store.Remove(ctx, first.ID, unused); err != nil {
		t.Fatalf("leaked lease: %v", err)
	}
}
