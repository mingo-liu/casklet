//go:build linux || darwin

package rootfs

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestCopyRejectsFIFO(t *testing.T) {
	source := template(t)
	if err := unix.Mkfifo(filepath.Join(source, "fifo"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Copy(context.Background(), source, t.TempDir()); err == nil {
		t.Fatal("FIFO was accepted")
	}
}

func TestCopyFileDoesNotFollowSymlinks(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target")
	link := filepath.Join(directory, "link")
	must(t, os.WriteFile(target, []byte("secret"), 0600))
	must(t, os.Symlink(target, link))
	if err := copyFile(context.Background(), link, filepath.Join(directory, "copy"), 0600, nil); err == nil {
		t.Fatal("copyFile followed a source symlink")
	}
}

func TestPinnedSourceRejectsEscapingAncestor(t *testing.T) {
	source := t.TempDir()
	outside := t.TempDir()
	must(t, os.Mkdir(filepath.Join(source, "directory"), 0755))
	must(t, os.WriteFile(filepath.Join(outside, "file"), []byte("host-secret"), 0600))
	root, err := os.OpenRoot(source)
	must(t, err)
	defer root.Close()
	must(t, os.Remove(filepath.Join(source, "directory")))
	must(t, os.Symlink(outside, filepath.Join(source, "directory")))
	if file, err := openRootSource(root, "directory/file"); err == nil {
		file.Close()
		t.Fatal("pinned source read outside its root through a replaced ancestor")
	}
}
