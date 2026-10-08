//go:build linux

package container

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mingo-liu/casklet/internal/rootfs"
	"golang.org/x/sys/unix"
)

// This stable lock lives outside transaction directories. It fences recursive
// deletion even after RemoveAll has unlinked a transaction's own lease files.
// Waiting for deletion never holds the metadata lock. Opportunistic recovery
// skips a live deleter instead of delaying unrelated commands.
func (store *Store) transactionLock(ctx context.Context, wait bool) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := store.checkDirectory(store.root, true); err != nil {
		return nil, err
	}
	file, err := store.openFile(filepath.Join(store.root, ".transactions"), unix.O_RDWR, true)
	if err != nil {
		return nil, err
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			file.Close()
			return nil, err
		}
		err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return file, nil
		}
		if !wait || !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EINTR) {
			file.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			file.Close()
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func transactionName(name string) bool {
	for _, prefix := range []string{".create-", ".remove-"} {
		if id, ok := strings.CutPrefix(name, prefix); ok && validateID(id) == nil {
			return true
		}
	}
	return false
}

func closeTransactionFiles(files []*os.File) {
	for _, file := range files {
		file.Close()
	}
}

// Called under the metadata lock: live creation cannot publish or mutate its
// staging directory while recovery claims it. Missing leases are allowed only
// for partial transactions; present leases must be safe and unclaimed.
func (store *Store) claimTransaction(path string) ([]*os.File, error) {
	if filepath.Dir(path) != store.root || !transactionName(filepath.Base(path)) {
		return nil, errors.New("invalid container transaction path")
	}
	if err := store.checkDirectory(path, true); err != nil {
		return nil, err
	}
	var files []*os.File
	for _, name := range []string{".lease", ".operation", ".logs"} {
		file, err := store.openFile(filepath.Join(path, name), unix.O_RDWR, false)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err == nil {
			files = append(files, file)
			err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		}
		if err != nil {
			closeTransactionFiles(files)
			return nil, err
		}
	}
	if err := rootfs.CheckUnmounted(path); err != nil {
		closeTransactionFiles(files)
		return nil, err
	}
	return files, nil
}

func (store *Store) recoverTransactions(ctx context.Context) error {
	deletion, err := store.transactionLock(ctx, false)
	if errors.Is(err, unix.EWOULDBLOCK) {
		return nil
	}
	if err != nil {
		return err
	}
	defer deletion.Close()
	lock, err := store.lock(ctx, true)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(store.root)
	lock.Close()
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !transactionName(entry.Name()) {
			continue
		}
		path := filepath.Join(store.root, entry.Name())
		lock, err := store.lock(ctx, false)
		if err != nil {
			return err
		}
		files, err := store.claimTransaction(path)
		lock.Close()
		// Creation may have published since ReadDir, or a lease may still be live.
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, unix.EWOULDBLOCK) {
			continue
		}
		if err != nil {
			return fmt.Errorf("preserve container transaction %s: %w", entry.Name(), err)
		}
		err = ctx.Err()
		if err == nil {
			err = os.RemoveAll(path)
		}
		closeTransactionFiles(files)
		if err != nil {
			return err
		}
		if err := syncDirectory(store.root); err != nil {
			return err
		}
	}
	return nil
}
