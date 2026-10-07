//go:build linux

package template

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func builtinParent(t *testing.T) string {
	t.Helper()
	path := t.TempDir()
	if err := os.Chmod(path, 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func builtinFixture(t *testing.T, path string) {
	t.Helper()
	root := testRootFS(t)
	for _, dir := range []string{"etc", "usr/bin"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0755); err != nil {
			t.Fatal(err)
		}
	}
	for _, dir := range []string{".", "usr"} {
		if err := os.Chmod(filepath.Join(root, dir), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(filepath.Join(root, "tmp"), os.ModeSticky|0777); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"sh", "cat"} {
		if err := os.Symlink("busybox", filepath.Join(root, "bin", name)); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"passwd", "group"} {
		if err := os.WriteFile(filepath.Join(root, "etc", name), []byte("root fixture\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(filepath.Join(root, "bin/busybox"))
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := json.Marshal(builtinMetadata{Architecture: runtime.GOARCH, Package: "busybox-static", Version: "fixture", SHA256: fmt.Sprintf("%x", sha256.Sum256(data)), Applets: []string{"cat", "sh"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".mini-docker-rootfs.json"), metadata, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(root, path); err != nil {
		t.Fatal(err)
	}
}

func assertNoBuiltinStages(t *testing.T, parent string) {
	t.Helper()
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), builtinStagePrefix) {
			t.Fatalf("unreclaimed staging tree: %s", entry.Name())
		}
	}
}

func TestBuiltinRepairAndHealthyReuse(t *testing.T) {
	for _, failure := range []string{"absent", "missing busybox", "nonexecutable busybox", "corrupt binary", "missing applet", "wrong applet link", "missing directory", "invalid metadata", "legacy metadata", "unexpected entry"} {
		t.Run(failure, func(t *testing.T) {
			path := filepath.Join(builtinParent(t), "busybox")
			owner := uint32(os.Geteuid())
			if failure != "absent" {
				builtinFixture(t, path)
				var err error
				switch failure {
				case "missing busybox":
					err = os.Remove(filepath.Join(path, "bin/busybox"))
				case "nonexecutable busybox":
					err = os.Chmod(filepath.Join(path, "bin/busybox"), 0644)
				case "corrupt binary":
					file, openErr := os.OpenFile(filepath.Join(path, "bin/busybox"), os.O_APPEND|os.O_WRONLY, 0)
					if openErr != nil {
						t.Fatal(openErr)
					}
					_, err = file.WriteString("corrupted")
					err = errors.Join(err, file.Close())
				case "missing applet":
					err = os.Remove(filepath.Join(path, "bin/cat"))
				case "wrong applet link":
					err = os.Remove(filepath.Join(path, "bin/sh"))
					if err == nil {
						err = os.Symlink("/outside", filepath.Join(path, "bin/sh"))
					}
				case "missing directory":
					err = os.Remove(filepath.Join(path, "proc"))
				case "invalid metadata":
					err = os.WriteFile(filepath.Join(path, ".mini-docker-rootfs.json"), []byte("invalid"), 0644)
				case "legacy metadata":
					err = os.WriteFile(filepath.Join(path, ".mini-docker-rootfs.json"), []byte(`{"package":"busybox-static"}`), 0644)
				case "unexpected entry":
					err = os.WriteFile(filepath.Join(path, "extra"), []byte("unexpected"), 0600)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			prepare := func(_ context.Context, destination string) error {
				calls++
				builtinFixture(t, destination)
				return nil
			}
			if err := ensureBuiltinAt(context.Background(), path, owner, prepare); err != nil {
				t.Fatal(err)
			}
			if err := validateBuiltin(context.Background(), path, owner); err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := ensureBuiltinAt(context.Background(), path, owner, prepare); err != nil {
				t.Fatal(err)
			}
			after, err := os.Stat(path)
			if err != nil || !os.SameFile(before, after) || calls != 1 {
				t.Fatalf("healthy source was replaced: calls=%d error=%v", calls, err)
			}
			assertNoBuiltinStages(t, filepath.Dir(path))
		})
	}
}

func TestBuiltinFailedPreparationPreservesOldTree(t *testing.T) {
	for _, failure := range []string{"prepare error", "invalid candidate", "cancellation"} {
		t.Run(failure, func(t *testing.T) {
			path := filepath.Join(builtinParent(t), "busybox")
			builtinFixture(t, path)
			if err := os.Remove(filepath.Join(path, "bin/cat")); err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cause := errors.New("prepare failed")
			err = ensureBuiltinAt(ctx, path, uint32(os.Geteuid()), func(_ context.Context, candidate string) error {
				builtinFixture(t, candidate)
				switch failure {
				case "prepare error":
					return cause
				case "invalid candidate":
					return os.Remove(filepath.Join(candidate, "bin/sh"))
				default:
					cancel()
					return nil
				}
			})
			if err == nil || (failure == "prepare error" && !errors.Is(err, cause)) || (failure == "cancellation" && !errors.Is(err, context.Canceled)) {
				t.Fatalf("repair error = %v", err)
			}
			after, statErr := os.Stat(path)
			if statErr != nil || !os.SameFile(before, after) {
				t.Fatalf("failed repair replaced original: %v", statErr)
			}
			assertNoBuiltinStages(t, filepath.Dir(path))
		})
	}
}

func TestBuiltinRecoveryAndUnsafeTreePreservation(t *testing.T) {
	for _, kind := range []string{"stale stage", "template symlink", "stage symlink", "public directory", "lease symlink", "lease hardlink"} {
		t.Run(kind, func(t *testing.T) {
			parent := builtinParent(t)
			path := filepath.Join(parent, "busybox")
			outside := t.TempDir()
			marker := filepath.Join(outside, "preserved")
			if err := os.WriteFile(marker, []byte("preserved"), 0644); err != nil {
				t.Fatal(err)
			}
			var setupErr error
			switch kind {
			case "stale stage":
				stage := filepath.Join(parent, builtinStagePrefix+"abandoned")
				setupErr = os.Mkdir(stage, 0700)
				if setupErr == nil {
					setupErr = os.Symlink(outside, filepath.Join(stage, "external"))
				}
			case "template symlink":
				setupErr = os.Symlink(outside, path)
			case "stage symlink":
				setupErr = os.Symlink(outside, filepath.Join(parent, builtinStagePrefix+"unsafe"))
			case "public directory":
				setupErr = os.Chmod(parent, 0777)
			case "lease symlink":
				setupErr = os.Symlink(marker, filepath.Join(parent, ".busybox.lock"))
			case "lease hardlink":
				setupErr = os.Link(marker, filepath.Join(parent, ".busybox.lock"))
			}
			if setupErr != nil {
				t.Fatal(setupErr)
			}
			calls := 0
			err := ensureBuiltinAt(context.Background(), path, uint32(os.Geteuid()), func(_ context.Context, candidate string) error {
				calls++
				builtinFixture(t, candidate)
				return nil
			})
			if kind == "stale stage" {
				if err != nil || calls != 1 {
					t.Fatalf("stale recovery: %v, calls=%d", err, calls)
				}
				assertNoBuiltinStages(t, parent)
			} else if err == nil || calls != 0 {
				t.Fatalf("unsafe tree accepted: %v, calls=%d", err, calls)
			}
			data, err := os.ReadFile(marker)
			if err != nil || string(data) != "preserved" {
				t.Fatalf("modified outside file: %q, %v", data, err)
			}
		})
	}
}

func TestBuiltinCopyLeaseFencesRepairAndReleasesIndependently(t *testing.T) {
	parent := builtinParent(t)
	owner := uint32(os.Geteuid())
	writer, err := lockBuiltinAt(context.Background(), parent, owner, true)
	if err != nil {
		t.Fatal(err)
	}
	writer.Close()
	reader, err := lockBuiltinAt(context.Background(), parent, owner, false)
	if err != nil {
		t.Fatal(err)
	}
	imageLease := &testLease{}
	source := &Template{copyLease: reader, lease: imageLease}
	defer source.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if lease, err := lockBuiltinAt(ctx, parent, owner, true); lease != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("copy was not fenced: lease=%v, error=%v", lease, err)
	}
	if err := source.ReleaseCopyLease(); err != nil || imageLease.closed != 0 {
		t.Fatalf("copy release closed image: %v, closes=%d", err, imageLease.closed)
	}
	if err := source.ReleaseCopyLease(); err != nil {
		t.Fatal(err)
	}
	writer, err = lockBuiltinAt(context.Background(), parent, owner, true)
	if err != nil {
		t.Fatal(err)
	}
	writer.Close()
}

func TestCanceledBuiltinAcquisitionDoesNotReturnLease(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	lease, err := lockBuiltin(ctx, false)
	if lease != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled acquisition: lease=%v, error=%v", lease, err)
	}
}

func TestConcurrentBuiltinRepairPublishesOnce(t *testing.T) {
	path := filepath.Join(builtinParent(t), "busybox")
	var wait sync.WaitGroup
	errorsCh := make(chan error, 4)
	var calls atomic.Int32
	for range 4 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			errorsCh <- ensureBuiltinAt(context.Background(), path, uint32(os.Geteuid()), func(_ context.Context, candidate string) error {
				calls.Add(1)
				builtinFixture(t, candidate)
				return nil
			})
		}()
	}
	wait.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("concurrent repairs generated %d templates", calls.Load())
	}
}
