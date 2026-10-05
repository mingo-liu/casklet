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
	if err := copyFile(context.Background(), link, filepath.Join(directory, "copy"), 0600); err == nil {
		t.Fatal("copyFile followed a source symlink")
	}
}
