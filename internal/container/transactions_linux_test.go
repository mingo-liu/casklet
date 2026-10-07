//go:build linux

package container

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func transactionFixture(t *testing.T, store *Store, prefix string) string {
	t.Helper()
	path := filepath.Join(store.root, prefix+strings.Repeat("b", 32))
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(path, "rootfs"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "rootfs", "data"), []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestStoreOperationsRecoverInterruptedTransactions(t *testing.T) {
	for _, operation := range []string{"create", "list", "remove"} {
		t.Run(operation, func(t *testing.T) {
			store := testStore(t)
			record := createTestRecord(t, store, "retained")
			finishTestExecution(t, store, record, 0)
			created := transactionFixture(t, store, ".create-")
			removed := transactionFixture(t, store, ".remove-")
			outside := t.TempDir()
			marker := filepath.Join(outside, "keep")
			if err := os.WriteFile(marker, []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(removed, "rootfs", "external")); err != nil {
				t.Fatal(err)
			}
			unknown := filepath.Join(store.root, ".remove-local-not-a-transaction")
			if err := os.Mkdir(unknown, 0700); err != nil {
				t.Fatal(err)
			}
			var err error
			switch operation {
			case "create":
				_, err = store.Create(context.Background(), testConfig(), "next")
			case "list":
				_, err = store.List(context.Background())
			case "remove":
				err = store.Remove(context.Background(), record.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{created, removed} {
				if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("transaction survived: %s: %v", path, err)
				}
			}
			for _, path := range []string{marker, unknown} {
				if _, err := os.Lstat(path); err != nil {
					t.Fatalf("recovery changed unrelated data: %v", err)
				}
			}
		})
	}
}

func TestRecoveryPreservesLeasedTransactions(t *testing.T) {
	for _, name := range []string{".lease", ".operation", ".logs"} {
		t.Run(name, func(t *testing.T) {
			store := testStore(t)
			path := transactionFixture(t, store, ".remove-")
			file, err := store.openFile(filepath.Join(path, name), unix.O_RDWR, true)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
				t.Fatal(err)
			}
			if _, err := store.List(context.Background()); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatal("live transaction removed", err)
			}
			file.Close()
			if _, err := store.List(context.Background()); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("abandoned transaction not reclaimed", err)
			}
		})
	}
}

func TestLiveDeletionDoesNotBlockMetadataLogsOrRecoverItsPartialTree(t *testing.T) {
	store, record, _, log := rotationFixture(t, 3)
	deletion, err := store.transactionLock(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	defer deletion.Close()
	// A live RemoveAll may already have removed every internal lease file.
	partial := transactionFixture(t, store, ".remove-")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := store.List(ctx); err != nil {
		t.Fatal("live deletion blocked listing", err)
	}
	if err := store.Update(ctx, record.ID, func(r *Record) error { r.State = StateExited; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(ctx, testConfig(), "independent"); err != nil {
		t.Fatal("live deletion blocked creation", err)
	}
	if _, err := log.Write([]byte("independent output\n")); err != nil {
		t.Fatal("live deletion blocked logging", err)
	}
	if _, err := os.Stat(partial); err != nil {
		t.Fatal("recovery raced active deletion", err)
	}
	short, stop := context.WithTimeout(ctx, 20*time.Millisecond)
	defer stop()
	if err := store.Remove(short, record.ID); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deletion ignored cancellation: %v", err)
	}
	deletion.Close()
	if err := store.Remove(ctx, record.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(partial); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("partial deletion not recovered", err)
	}
}

func TestTransactionRecoveryRejectsUnsafeArtifacts(t *testing.T) {
	for _, kind := range []string{"symlink", "public", "lease-symlink", "lease-hardlink", "lease-fifo", "lease-public"} {
		t.Run(kind, func(t *testing.T) {
			store := testStore(t)
			path := transactionFixture(t, store, ".remove-")
			outside := filepath.Join(t.TempDir(), "outside")
			if err := os.WriteFile(outside, []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			lease := filepath.Join(path, ".lease")
			var err error
			switch kind {
			case "symlink":
				if err := os.RemoveAll(path); err != nil {
					t.Fatal(err)
				}
				err = os.Symlink(filepath.Dir(outside), path)
			case "public":
				err = os.Chmod(path, 0755)
			case "lease-symlink":
				err = os.Symlink(outside, lease)
			case "lease-hardlink":
				err = os.Link(outside, lease)
			case "lease-fifo":
				err = unix.Mkfifo(lease, 0600)
			case "lease-public":
				err = os.WriteFile(lease, nil, 0644)
			}
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if _, err := store.List(ctx); err == nil {
				t.Fatal("unsafe transaction reclaimed")
			}
			if data, err := os.ReadFile(outside); err != nil || string(data) != "keep" {
				t.Fatal("unrelated data changed", err)
			}
			if _, err := os.Lstat(path); err != nil {
				t.Fatal("unsafe transaction was deleted", err)
			}
		})
	}
}

func TestTransactionRecoveryHonorsCancellation(t *testing.T) {
	store := testStore(t)
	path := transactionFixture(t, store, ".create-")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.recoverTransactions(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("canceled recovery deleted data", err)
	}
}
