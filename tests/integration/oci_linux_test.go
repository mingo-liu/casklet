//go:build linux

package integration

import (
	"archive/tar"
	"bytes"
	"context"
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
	"github.com/mingo-liu/mini-docker/internal/image"
)

// The registry fixture uses a relocated static toolbox instead of bin/busybox.
// No network registry, Docker Engine, or external image is required by CI.
func ociRegistryFixture(t *testing.T) (string, func()) {
	t.Helper()
	require(t)
	source := executionTemplate(t)
	var data bytes.Buffer
	writer := tar.NewWriter(&data)
	err := filepath.WalkDir(source, func(filename string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, filename)
		if err != nil {
			return err
		}
		if rel == "." || rel == "bin/busybox" || rel == "etc/passwd" || rel == "etc/group" {
			return nil
		}
		info, err := os.Lstat(filename)
		if err != nil {
			return err
		}
		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			link, err = os.Readlink(filename)
			if err != nil {
				return err
			}
			if link == "busybox" {
				link = "/usr/bin/toolbox"
			}
		}
		h, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		h.Name = filepath.ToSlash(rel)
		h.Uid, h.Gid = 0, 0
		if err := writer.WriteHeader(h); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			f, err := os.Open(filename)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(writer, f)
			f.Close()
			return copyErr
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	toolbox, err := os.ReadFile(filepath.Join(source, "bin/busybox"))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range []struct {
		name     string
		body     []byte
		uid, gid int
		kind     byte
		mode     int64
	}{
		{"usr/bin/toolbox", toolbox, 0, 0, tar.TypeReg, 0755},
		{"etc/passwd", []byte("root:x:0:0:root:/:/bin/sh\napp:x:123:456:app:/data:/bin/sh\n"), 0, 0, tar.TypeReg, 0644},
		{"etc/group", []byte("data:x:456:\n"), 0, 0, tar.TypeReg, 0644},
		{"data", nil, 123, 456, tar.TypeDir, 0755},
	} {
		h := &tar.Header{Name: file.name, Size: int64(len(file.body)), Typeflag: file.kind, Mode: file.mode, Uid: file.uid, Gid: file.gid}
		if err := writer.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(file.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	layer, err := tarball.LayerFromReader(bytes.NewReader(data.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	img, err := mutate.AppendLayers(empty.Image, layer)
	if err != nil {
		t.Fatal(err)
	}
	cf, err := img.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	cf.OS = "linux"
	cf.Architecture = runtime.GOARCH
	cf.Config = v1.Config{Entrypoint: []string{"/bin/sh", "-c"}, Cmd: []string{`printf '%s:%s:%s\n' "$IMAGE_VALUE" "$1" "$PWD"`, "entry", "default"}, Env: []string{"IMAGE_VALUE=image", "PATH=/usr/bin:/bin"}, WorkingDir: "/new-work", User: "app:data"}
	img, err = mutate.ConfigFile(img, cf)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(server.Close)
	ref, err := name.ParseReference(strings.TrimPrefix(server.URL, "http://") + "/mini-docker/app:test")
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(ref, img); err != nil {
		t.Fatal(err)
	}
	return ref.Name(), server.Close
}

func TestOCIImageAutomaticPullDefaultsOwnershipAndRetainedRestart(t *testing.T) {
	ref, closeRegistry := ociRegistryFixture(t)
	code, out, stderr := start(t, "", "run", "--image", ref).wait(t)
	if code != 0 || out != "image:default:/new-work\n" {
		t.Fatalf("default image run: %d %q %q", code, out, stderr)
	}
	store, err := image.OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Resolve(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Remove(context.Background(), record.ID, func(context.Context, string) (bool, error) { return false, nil }); err != nil {
			t.Errorf("remove fixture image: %v", err)
		}
	})
	closeRegistry()
	// Cached names and IDs work offline. UID/GID and writable ownership survive
	// extraction plus the second copy into a private container filesystem.
	code, out, stderr = start(t, "", "run", "--image", ref, "--entrypoint", "", "--env", "IMAGE_VALUE=override", "--", "/bin/sh", "-c", `set -eu; [ ! -e /bin/busybox ]; [ "$(id -u):$(id -g)" = 123:456 ]; [ "$(stat -c %u:%g /data)" = 123:456 ]; echo private > /data/file; echo work > /new-work/file; printf '%s\n' "$IMAGE_VALUE"; [ -d /dev/shm ]`).wait(t)
	if code != 0 || out != "override\n" {
		t.Fatalf("offline overrides: %d %q %q", code, out, stderr)
	}
	name := backgroundName(t)
	backgroundSuccess(t, "run", "-d", "--name", name, "--image", record.ID, "--entrypoint", "", "--", "/bin/sh", "-c", "echo ready; sleep 300")
	backgroundSuccess(t, "exec", name, "--", "/bin/sh", "-c", "echo retained > /data/persistent")
	backgroundSuccess(t, "stop", name)
	backgroundSuccess(t, "start", name)
	if out := backgroundSuccess(t, "exec", name, "--", "/bin/cat", "/data/persistent"); out != "retained\n" {
		t.Fatalf("retained image data: %q", out)
	}
	assertImageInUse(t, record.ID)
	backgroundSuccess(t, "stop", name)
	backgroundSuccess(t, "rm", name)
}

func TestOCIRootEntrypointCapabilities(t *testing.T) {
	ref, _ := ociRegistryFixture(t)
	// Override the fixture's non-root User, as root application entrypoints do.
	code, out, stderr := start(t, "", "run", "--image", ref, "--user", "0:0", "--entrypoint", "", "--", "/bin/sh", "-c", `set -eu; chown 321:654 /data; [ "$(stat -c %u:%g /data)" = 321:654 ]; grep '^NoNewPrivs:[[:space:]]*1$' /proc/self/status; grep '^CapEff:[[:space:]]*00000000000005eb$' /proc/self/status; mkdir /tmp/mount-check; if mount -t tmpfs tmpfs /tmp/mount-check; then exit 1; fi; /bin/integration-helper image-privilege-drop; echo image-root-ok`).wait(t)
	if code != 0 || !strings.Contains(out, "image-root-ok") {
		t.Fatalf("image privileges: %d %q %q", code, out, stderr)
	}
	store, err := image.OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Resolve(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Remove(context.Background(), record.ID, func(context.Context, string) (bool, error) { return false, nil }); err != nil {
		t.Fatal(err)
	}
}
