//go:build linux

package image

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func cleanupTemplate(t *testing.T, value string) string {
	t.Helper()
	source := testTemplate(t)
	if err := os.WriteFile(filepath.Join(source, "value"), []byte(value), 0644); err != nil {
		t.Fatal(err)
	}
	return source
}

func cleanupPeer(t *testing.T, store *Store) *Store {
	t.Helper()
	peer, err := newStoreAt(store.root)
	if err != nil {
		t.Fatal(err)
	}
	return peer
}

func awaitImageCleanup(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("image cleanup did not finish")
		return nil
	}
}

func pauseImageCleanup(t *testing.T, store *Store) (<-chan string, func()) {
	t.Helper()
	started, release := make(chan string, 1), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	store.removeTreeFn = func(ctx context.Context, path string) error {
		if strings.HasPrefix(filepath.Base(path), ".delete-") {
			// Expose the half-removed window: the internal flock file is
			// already gone while most of the image tree still exists.
			if err := os.Remove(filepath.Join(path, ".lease")); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			started <- path
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return removeImageTree(ctx, path)
	}
	return started, unblock
}

func cleanupStarted(t *testing.T, started <-chan string, finished <-chan error) string {
	t.Helper()
	select {
	case path := <-started:
		return path
	case <-time.After(5 * time.Second):
		select {
		case err := <-finished:
			t.Fatalf("image cleanup finished before its barrier: %v", err)
		default:
			t.Fatal("image cleanup did not reach its barrier")
		}
	}
	return ""
}

func assertCleanupAllowsUnrelatedOperations(t *testing.T, peer *Store, cached Record) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := peer.List(ctx); err != nil {
		t.Fatalf("list blocked by recursive cleanup: %v", err)
	}
	if _, _, lease, err := peer.Acquire(ctx, cached.ID); err != nil {
		t.Fatalf("cached acquire blocked by recursive cleanup: %v", err)
	} else {
		lease.Close()
	}
	if _, err := peer.Import(ctx, cleanupTemplate(t, "unrelated import")); err != nil {
		t.Fatalf("import blocked by recursive cleanup: %v", err)
	}
	img := fixtureImage(t, runtime.GOARCH, layerBytes(t, tarEntry{name: "app", body: "unrelated pull"}))
	if _, err := peer.pullImage(ctx, "localhost/unrelated-cleanup:v1", img); err != nil {
		t.Fatalf("pull blocked by recursive cleanup: %v", err)
	}
	if err := peer.recover(ctx); err != nil {
		t.Fatalf("active cleanup confused recovery: %v", err)
	}
}

func assertCleanupArtifactsGone(t *testing.T, store *Store) {
	t.Helper()
	entries, err := os.ReadDir(store.root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		for _, prefix := range []string{".delete-", ".transaction-", ".prepare-", ".import-", ".remove-"} {
			if strings.HasPrefix(entry.Name(), prefix) {
				t.Errorf("abandoned cleanup artifact remains: %s", entry.Name())
			}
		}
	}
}

func TestImageDeletionOutsideGlobalLockKeepsHalfRemovedTreeLeased(t *testing.T) {
	store := testStore(t)
	source := cleanupTemplate(t, "deleted then republished")
	removed, err := store.Import(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	cached, err := store.Import(context.Background(), cleanupTemplate(t, "cached reader"))
	if err != nil {
		t.Fatal(err)
	}
	peer := cleanupPeer(t, store)
	started, unblock := pauseImageCleanup(t, store)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- store.Remove(ctx, removed.ID, unused) }()
	tombstone := cleanupStarted(t, started, finished)
	if _, err := os.Stat(filepath.Join(tombstone, ".lease")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("half-removal fixture retained its internal lease: %v", err)
	}
	if _, _, _, err := peer.Acquire(context.Background(), removed.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("hidden image still published: %v", err)
	}
	assertCleanupAllowsUnrelatedOperations(t, peer, cached)
	if _, err := os.Stat(filepath.Join(tombstone, "rootfs", "value")); err != nil {
		t.Fatalf("another operation recovered a live half-removed tree: %v", err)
	}
	// A new image with the same immutable identity owns a different usage lease
	// and directory. Finishing old cleanup must never remove that image.
	replacement, err := peer.Import(context.Background(), source)
	if err != nil || replacement.ID != removed.ID {
		t.Fatalf("same-ID publication blocked by old cleanup: %+v, %v", replacement, err)
	}
	// Older engines only recognize .import/.remove. Their recovery cannot claim
	// this deletion after its internal lease has disappeared.
	lock, err := peer.lock(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(peer.root)
	if err != nil {
		lock.Close()
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".import-") || strings.HasPrefix(entry.Name(), ".remove-") {
			if err := os.RemoveAll(filepath.Join(peer.root, entry.Name())); err != nil {
				lock.Close()
				t.Fatal(err)
			}
		}
	}
	lock.Close()
	if _, err := os.Stat(tombstone); err != nil {
		t.Fatalf("legacy recovery removed active deletion: %v", err)
	}
	unblock()
	if err := awaitImageCleanup(t, finished); err != nil {
		t.Fatal(err)
	}
	if _, _, lease, err := peer.Acquire(context.Background(), replacement.ID); err != nil {
		t.Fatalf("old cleanup removed replacement image: %v", err)
	} else {
		lease.Close()
	}
	assertCleanupArtifactsGone(t, peer)
}

func TestAbandonedLegacyRecoveryRunsOutsideGlobalLock(t *testing.T) {
	store := testStore(t)
	cached, err := store.Import(context.Background(), cleanupTemplate(t, "cached during recovery"))
	if err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(store.root, ".remove-legacy-abandoned")
	if err := os.Mkdir(legacy, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, ".lease"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "remaining"), []byte("preserved until owner finishes"), 0600); err != nil {
		t.Fatal(err)
	}
	started, unblock := pauseImageCleanup(t, store)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- store.recover(ctx) }()
	tombstone := cleanupStarted(t, started, finished)
	if _, err := os.Stat(legacy); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy recovery did not claim its tree: %v", err)
	}
	peer := cleanupPeer(t, store)
	assertCleanupAllowsUnrelatedOperations(t, peer, cached)
	if _, err := os.Stat(filepath.Join(tombstone, "remaining")); err != nil {
		t.Fatalf("active recovery lost its partially cleaned tree: %v", err)
	}
	unblock()
	if err := awaitImageCleanup(t, finished); err != nil {
		t.Fatal(err)
	}
	assertCleanupArtifactsGone(t, peer)
}

func TestCanceledAndFailedImageDeletionCanBeRecovered(t *testing.T) {
	for _, failure := range []string{"canceled", "deadline", "storage failure"} {
		t.Run(failure, func(t *testing.T) {
			store := testStore(t)
			source := cleanupTemplate(t, "recover canceled deletion")
			record, err := store.Import(context.Background(), source)
			if err != nil {
				t.Fatal(err)
			}
			started := make(chan string, 1)
			injected := errors.New("injected image cleanup failure")
			store.removeTreeFn = func(ctx context.Context, path string) error {
				if err := os.Remove(filepath.Join(path, ".lease")); err != nil {
					return err
				}
				started <- path
				if failure == "storage failure" {
					return injected
				}
				<-ctx.Done()
				return ctx.Err()
			}
			ctx, cancel := context.WithCancel(context.Background())
			want := error(context.Canceled)
			if failure == "deadline" {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 500*time.Millisecond)
				want = context.DeadlineExceeded
			} else if failure == "storage failure" {
				want = injected
			}
			defer cancel()
			finished := make(chan error, 1)
			go func() { finished <- store.Remove(ctx, record.ID, unused) }()
			tombstone := cleanupStarted(t, started, finished)
			if failure == "canceled" {
				cancel()
			}
			if err := awaitImageCleanup(t, finished); !errors.Is(err, want) {
				t.Fatalf("cleanup failure lost cause: %v, want %v", err, want)
			}
			if _, err := os.Stat(tombstone); err != nil {
				t.Fatalf("failed cleanup did not retain its tree: %v", err)
			}
			if _, err := os.Stat(store.transactionPath(tombstone)); err != nil {
				t.Fatalf("failed cleanup lost its stable recovery lock: %v", err)
			}
			peer := cleanupPeer(t, store)
			if replacement, err := peer.Import(context.Background(), source); err != nil || replacement.ID != record.ID {
				t.Fatalf("retry could not recover and republish: %+v, %v", replacement, err)
			}
			assertCleanupArtifactsGone(t, peer)
		})
	}
}

func TestRecoveryClaimsLegacyAndAbandonedPreparationWithoutFollowingLinks(t *testing.T) {
	for _, prefix := range []string{".import-", ".remove-", ".prepare-"} {
		t.Run(prefix, func(t *testing.T) {
			store := testStore(t)
			stage := filepath.Join(store.root, prefix+"abandoned")
			if err := os.Mkdir(stage, 0700); err != nil {
				t.Fatal(err)
			}
			outside := t.TempDir()
			if err := os.WriteFile(filepath.Join(outside, "keep"), []byte("outside"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(stage, "external")); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(store.transactionPath(stage), nil, 0600); err != nil {
				t.Fatal(err)
			}
			if err := store.recover(context.Background()); err != nil {
				t.Fatal(err)
			}
			if data, err := os.ReadFile(filepath.Join(outside, "keep")); err != nil || string(data) != "outside" {
				t.Fatalf("recovery followed a symlink: %q, %v", data, err)
			}
			assertCleanupArtifactsGone(t, store)
		})
	}
}

func TestRecoveryRejectsUnsafeTransactionsAndLeavesUnknownPaths(t *testing.T) {
	for _, artifact := range []string{"directory symlink", "public directory", "public external lock", "external lock symlink", "external lock hardlink", "external lock fifo", "delete without external lock"} {
		t.Run(artifact, func(t *testing.T) {
			store := testStore(t)
			stage := filepath.Join(store.root, ".delete-unsafe")
			if err := os.Mkdir(stage, 0700); err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(t.TempDir(), "keep")
			if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
				t.Fatal(err)
			}
			transaction := store.transactionPath(stage)
			if artifact != "delete without external lock" {
				if err := os.WriteFile(transaction, nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			var err error
			switch artifact {
			case "directory symlink":
				if err := os.Remove(stage); err != nil {
					t.Fatal(err)
				}
				err = os.Symlink(filepath.Dir(outside), stage)
			case "public directory":
				err = os.Chmod(stage, 0755)
			case "public external lock":
				err = os.Chmod(transaction, 0644)
			case "external lock symlink", "external lock hardlink", "external lock fifo":
				if err := os.Remove(transaction); err != nil {
					t.Fatal(err)
				}
				if artifact == "external lock symlink" {
					err = os.Symlink(outside, transaction)
				} else if artifact == "external lock hardlink" {
					err = os.Link(outside, transaction)
				} else {
					err = unix.Mkfifo(transaction, 0600)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := store.recover(context.Background()); err == nil {
				t.Fatal("unsafe transaction accepted")
			}
			if _, err := os.Lstat(stage); err != nil {
				t.Fatalf("unsafe artifact deleted: %v", err)
			}
			if data, err := os.ReadFile(outside); err != nil || string(data) != "outside" {
				t.Fatalf("unsafe recovery modified outside data: %q, %v", data, err)
			}
		})
	}
	store := testStore(t)
	unknown := filepath.Join(store.root, "unrecognized-storage")
	if err := os.Mkdir(unknown, 0700); err != nil {
		t.Fatal(err)
	}
	if err := store.recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(unknown); err != nil {
		t.Fatalf("recovery removed an unknown directory: %v", err)
	}
}

func TestImageTreeRemovalChecksCancellationAndDoesNotFollowTopSymlink(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "keep"), []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := removeImageTree(ctx, root); !errors.Is(err, context.Canceled) {
		t.Fatalf("tree removal ignored cancellation: %v", err)
	}
	link := filepath.Join(t.TempDir(), "linked-root")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	if err := removeImageTree(context.Background(), link); err == nil {
		t.Fatal("tree removal followed its root symlink")
	}
	if _, err := os.Stat(filepath.Join(root, "keep")); err != nil {
		t.Fatalf("refused removal lost data: %v", err)
	}
}

func TestImageTreeRemovalDrainsMultipleDirectoryBatches(t *testing.T) {
	root := t.TempDir()
	for i := range 400 {
		dir := filepath.Join(root, fmt.Sprintf("child-%03d", i))
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "data"), []byte("remove"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := removeImageTree(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("multi-batch cleanup retained its tree: %v", err)
	}
}

func TestRecoveryPreservesLeasedLegacyTransactions(t *testing.T) {
	for _, prefix := range []string{".import-", ".remove-"} {
		t.Run(prefix, func(t *testing.T) {
			store := testStore(t)
			stage := filepath.Join(store.root, prefix+"leased")
			if err := os.Mkdir(stage, 0700); err != nil {
				t.Fatal(err)
			}
			lease, err := store.openFile(filepath.Join(stage, ".lease"), unix.O_RDWR|unix.O_CREAT|unix.O_EXCL)
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Close()
			if err := unix.Flock(int(lease.Fd()), unix.LOCK_SH|unix.LOCK_NB); err != nil {
				t.Fatal(err)
			}
			if err := store.recover(context.Background()); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(stage); err != nil {
				t.Fatalf("recovery claimed a leased legacy transaction: %v", err)
			}
			lease.Close()
			if err := store.recover(context.Background()); err != nil {
				t.Fatal(err)
			}
			assertCleanupArtifactsGone(t, store)
		})
	}
}

func TestRecoveryPreservesMountedTransactions(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("mounted recovery regression requires root")
	}
	store := testStore(t)
	stage := filepath.Join(store.root, ".import-mounted")
	target := filepath.Join(stage, "mounted")
	if err := os.Mkdir(stage, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mount("tmpfs", target, "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "size=1m"); err != nil {
		if errors.Is(err, unix.EPERM) {
			t.Skip("mounted recovery regression requires mount capabilities")
		}
		t.Fatal(err)
	}
	defer unix.Unmount(target, 0)
	if err := os.WriteFile(filepath.Join(target, "keep"), []byte("mounted data"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.recover(context.Background()); err == nil {
		t.Fatal("recovery accepted a mounted legacy transaction")
	}
	if data, err := os.ReadFile(filepath.Join(target, "keep")); err != nil || string(data) != "mounted data" {
		t.Fatalf("mounted data changed: %q, %v", data, err)
	}
}

func TestAbandonedRecoveryBoundsClaimedTransactionDescriptors(t *testing.T) {
	store := testStore(t)
	for i := range recoveryBatchSize*2 + 1 {
		if err := os.Mkdir(filepath.Join(store.root, fmt.Sprintf(".import-abandoned-%03d", i)), 0700); err != nil {
			t.Fatal(err)
		}
	}
	peak := 0
	store.removeTreeFn = func(ctx context.Context, path string) error {
		entries, err := os.ReadDir(store.root)
		if err != nil {
			return err
		}
		claimed := 0
		for _, entry := range entries {
			if !strings.HasPrefix(entry.Name(), ".transaction-.delete-") {
				continue
			}
			file, err := store.openFile(filepath.Join(store.root, entry.Name()), unix.O_RDONLY)
			if err != nil {
				return err
			}
			err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
			file.Close()
			if errors.Is(err, unix.EWOULDBLOCK) {
				claimed++
			} else if err != nil {
				return err
			}
		}
		if claimed > peak {
			peak = claimed
		}
		if claimed > recoveryBatchSize {
			return fmt.Errorf("claimed %d cleanup descriptors", claimed)
		}
		return removeImageTree(ctx, path)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := store.recover(ctx); err != nil {
		t.Fatal(err)
	}
	if peak != recoveryBatchSize {
		t.Fatalf("recovery did not exercise a complete bounded batch: %d", peak)
	}
	assertCleanupArtifactsGone(t, store)
}
