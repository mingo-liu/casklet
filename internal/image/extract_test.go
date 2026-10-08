package image

import (
	"archive/tar"
	"bytes"
	"context"
	"github.com/mingo-liu/casklet/internal/rootfs"
	"os"
	"path/filepath"
	"testing"
)

type tarEntry struct {
	name, body, link string
	kind             byte
	mode             int64
}

func TestLayerReassignsDirectoryOwnership(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to verify numeric image ownership transitions")
	}
	tree := t.TempDir()
	if err := os.Mkdir(filepath.Join(tree, "data"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(filepath.Join(tree, "data"), 123, 456); err != nil {
		t.Fatal(err)
	}
	// A directory header owned by root must override its lower-layer owner even
	// though the requested owner matches the extracting process's UID/GID.
	if err := testApply(t, tree, tarEntry{name: "data", kind: tar.TypeDir}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(tree, "data"))
	if err != nil {
		t.Fatal(err)
	}
	uid, gid := rootfs.Ownership(info)
	if uid != uint32(os.Geteuid()) || gid != uint32(os.Getegid()) {
		t.Fatalf("directory owner remained %d:%d", uid, gid)
	}
}

func layerBytes(t *testing.T, entries ...tarEntry) []byte {
	t.Helper()
	var data bytes.Buffer
	w := tar.NewWriter(&data)
	for _, entry := range entries {
		kind := entry.kind
		if kind == 0 {
			kind = tar.TypeReg
		}
		mode := entry.mode
		if mode == 0 {
			mode = 0755
		}
		header := &tar.Header{Name: entry.name, Linkname: entry.link, Typeflag: kind, Mode: mode, Uid: os.Geteuid(), Gid: os.Getegid()}
		if kind == tar.TypeReg {
			header.Size = int64(len(entry.body))
		}
		if err := w.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if header.Size > 0 {
			if _, err := w.Write([]byte(entry.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func testApply(t *testing.T, tree string, entries ...tarEntry) error {
	t.Helper()
	root, err := os.OpenRoot(tree)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	archive, err := os.CreateTemp(t.TempDir(), "layer-")
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	if _, err := archive.Write(layerBytes(t, entries...)); err != nil {
		t.Fatal(err)
	}
	return applyLayer(context.Background(), root, archive)
}

func TestLayersReplaceDeleteAndApplyOpaqueWhiteouts(t *testing.T) {
	tree := t.TempDir()
	if err := testApply(t, tree, tarEntry{name: "data/old", body: "old"}, tarEntry{name: "remove/sub/file", body: "removed"}, tarEntry{name: "base", body: "base"}, tarEntry{name: "retained-link", kind: tar.TypeLink, link: "base"}); err != nil {
		t.Fatal(err)
	}
	if err := testApply(t, tree,
		tarEntry{name: "data/new", body: "new"}, tarEntry{name: "data/.wh..wh..opq"},
		tarEntry{name: ".wh.remove"}, tarEntry{name: "base", body: "replacement"},
		tarEntry{name: "forward", kind: tar.TypeLink, link: "later"}, tarEntry{name: "later", body: "later"}); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"data/new": "new", "base": "replacement", "retained-link": "base", "forward": "later"} {
		got, err := os.ReadFile(filepath.Join(tree, name))
		if err != nil || string(got) != want {
			t.Fatalf("%s = %q, %v; want %q", name, got, err, want)
		}
	}
	for _, name := range []string{"data/old", "remove", "data/.wh..wh..opq", ".wh.remove"} {
		if _, err := os.Lstat(filepath.Join(tree, name)); !os.IsNotExist(err) {
			t.Fatalf("deleted entry remains: %s (%v)", name, err)
		}
	}
}

func TestLayersPreserveMergedUsrAndConfineAbsoluteLinks(t *testing.T) {
	tree, outside := t.TempDir(), t.TempDir()
	sentinel := filepath.Join(outside, "secret")
	if err := os.WriteFile(sentinel, []byte("host"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := testApply(t, tree, tarEntry{name: "usr/bin", kind: tar.TypeDir}, tarEntry{name: "bin", kind: tar.TypeSymlink, link: "/usr/bin"}, tarEntry{name: "escape", kind: tar.TypeSymlink, link: outside}); err != nil {
		t.Fatal(err)
	}
	if err := testApply(t, tree, tarEntry{name: "bin/app", body: "app"}, tarEntry{name: "escape/secret", body: "image"}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(tree, "usr/bin/app"))
	if err != nil || string(got) != "app" {
		t.Fatalf("merged usr: %q %v", got, err)
	}
	got, err = os.ReadFile(sentinel)
	if err != nil || string(got) != "host" {
		t.Fatalf("modified host through symlink: %q %v", got, err)
	}
	if link, err := os.Readlink(filepath.Join(tree, "bin")); err != nil || link != "/usr/bin" {
		t.Fatalf("link changed: %q %v", link, err)
	}
}

func TestLayersRejectUnsafeEntries(t *testing.T) {
	for name, entries := range map[string][]tarEntry{
		"traversal":           {{name: "../escape", body: "bad"}},
		"absolute":            {{name: "/escape", body: "bad"}},
		"symlink escape":      {{name: "link", kind: tar.TypeSymlink, link: "../escape"}},
		"hardlink escape":     {{name: "link", kind: tar.TypeLink, link: "../escape"}},
		"unresolved hardlink": {{name: "link", kind: tar.TypeLink, link: "missing"}},
		"device":              {{name: "device", kind: tar.TypeChar}},
		"fifo":                {{name: "fifo", kind: tar.TypeFifo}},
		"duplicate":           {{name: "a", body: "one"}, {name: "a", body: "two"}},
		"root replacement":    {{name: ".", body: "bad"}},
		"invalid whiteout":    {{name: ".wh.."}},
		"nonempty whiteout":   {{name: ".wh.file", body: "bad"}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := testApply(t, t.TempDir(), entries...); err == nil {
				t.Fatal("unsafe layer accepted")
			}
		})
	}
}

func TestLayerStripsPrivilegeBitsAndPreservesStickyMode(t *testing.T) {
	tree := t.TempDir()
	if err := testApply(t, tree, tarEntry{name: "app", body: "app", mode: 06755}, tarEntry{name: "shared", kind: tar.TypeDir, mode: 01777}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(tree, "app"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 || info.Mode().Perm() != 0755 {
		t.Fatalf("unsafe mode: %s", info.Mode())
	}
	info, err = os.Stat(filepath.Join(tree, "shared"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSticky == 0 {
		t.Fatal("sticky bit lost")
	}
}
