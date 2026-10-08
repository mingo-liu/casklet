//go:build linux

package integration

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/mingo-liu/casklet/internal/rootfs"
	"golang.org/x/sys/unix"
)

func TestTemplatePublicationPreservesMountedTrees(t *testing.T) {
	require(t)
	for _, kind := range []string{"destination root", "destination child", "candidate child"} {
		t.Run(kind, func(t *testing.T) {
			parent := t.TempDir()
			candidate := filepath.Join(parent, "candidate")
			destination := filepath.Join(parent, "destination")
			for _, path := range []string{candidate, destination} {
				if err := os.Mkdir(path, 0755); err != nil {
					t.Fatal(err)
				}
				if err := rootfs.Copy(context.Background(), template, path); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.Stat(destination)
			if err != nil {
				t.Fatal(err)
			}
			mount := destination
			if kind == "destination root" {
				err = unix.Mount(destination, destination, "", unix.MS_BIND, "")
			} else {
				mount = filepath.Join(destination, "proc")
				if kind == "candidate child" {
					mount = filepath.Join(candidate, "proc")
				}
				err = unix.Mount("tmpfs", mount, "tmpfs", 0, "size=1m")
			}
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := unix.Unmount(mount, unix.MNT_DETACH); err != nil {
					t.Error(err)
				}
			})
			if err := rootfs.Publish(context.Background(), candidate, destination); err == nil {
				t.Fatal("publication accepted a mounted template")
			}
			after, err := os.Stat(destination)
			if err != nil || !os.SameFile(before, after) {
				t.Fatalf("refused publication changed the destination: %v", err)
			}
			if _, err := os.Stat(filepath.Join(candidate, "bin/busybox")); err != nil {
				t.Fatalf("refused publication removed the candidate: %v", err)
			}
		})
	}
}
