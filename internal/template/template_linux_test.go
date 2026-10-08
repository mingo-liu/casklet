//go:build linux

package template

import (
	"context"
	"debug/elf"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/mingo-liu/casklet/internal/config"
)

func testRootFS(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"bin", "proc", "dev", "tmp"} {
		if err := os.Mkdir(filepath.Join(root, name), 0755); err != nil {
			t.Fatal(err)
		}
	}
	// Validate metadata without downloading or executing a BusyBox binary.
	header := make([]byte, 64)
	copy(header, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
	binary.LittleEndian.PutUint16(header[16:], uint16(elf.ET_EXEC))
	machine := elf.EM_X86_64
	if runtime.GOARCH == "arm64" {
		machine = elf.EM_AARCH64
	}
	binary.LittleEndian.PutUint16(header[18:], uint16(machine))
	binary.LittleEndian.PutUint32(header[20:], 1)
	binary.LittleEndian.PutUint16(header[52:], 64)
	if err := os.WriteFile(filepath.Join(root, "bin", "busybox"), header, 0755); err != nil {
		t.Fatal(err)
	}
	return root
}

type testLease struct {
	closed int
	err    error
}

func (l *testLease) Close() error { l.closed++; return l.err }

func TestAcquireDirectoryResolvesSymlinks(t *testing.T) {
	root := testRootFS(t)
	link := filepath.Join(t.TempDir(), "source")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	source, err := Acquire(context.Background(), config.Config{RootFS: link})
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if source.Path != root {
		t.Fatalf("source = %q, want %q", source.Path, root)
	}
}

func TestAcquireImageKeepsLeaseUntilClosed(t *testing.T) {
	root := testRootFS(t)
	lease := &testLease{}
	source, err := acquire(context.Background(), config.Config{RootFS: "/missing", Image: "saved-image"}, func(_ context.Context, id string) (string, io.Closer, error) {
		if id != "saved-image" {
			t.Fatalf("unexpected image %q", id)
		}
		return root, lease, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if source.Path != root || lease.closed != 0 {
		t.Fatalf("image source or live lease changed: %+v, closes = %d", source, lease.closed)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil || lease.closed != 1 {
		t.Fatalf("lease closes = %d, error = %v", lease.closed, err)
	}
}

func TestAcquireFailureReleasesLease(t *testing.T) {
	for _, failure := range []string{"invalid rootfs", "missing bind source", "overlapping bind source"} {
		t.Run(failure, func(t *testing.T) {
			root := testRootFS(t)
			cfg := config.Config{Image: "saved-image"}
			switch failure {
			case "invalid rootfs":
				if err := os.Remove(filepath.Join(root, "proc")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("/proc", filepath.Join(root, "proc")); err != nil {
					t.Fatal(err)
				}
			case "missing bind source":
				cfg.Mounts = []config.BindMount{{Source: filepath.Join(t.TempDir(), "missing"), Target: "/data"}}
			case "overlapping bind source":
				cfg.Mounts = []config.BindMount{{Source: root, Target: "/data"}}
			}
			closeErr := errors.New("close lease")
			lease := &testLease{err: closeErr}
			source, err := acquire(context.Background(), cfg, func(context.Context, string) (string, io.Closer, error) {
				return root, lease, nil
			})
			if source != nil || err == nil || lease.closed != 1 || !errors.Is(err, closeErr) {
				t.Fatalf("failed acquisition: source = %v, error = %v, closes = %d", source, err, lease.closed)
			}
		})
	}
}

func TestAcquireCanceledBeforeResolvingImage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	source, err := acquire(ctx, config.Config{Image: "saved-image"}, func(context.Context, string) (string, io.Closer, error) {
		t.Fatal("canceled acquisition opened the image store")
		return "", nil, nil
	})
	if source != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled acquisition: %v, %v", source, err)
	}
}
