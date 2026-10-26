//go:build linux

package rootfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// RemoveTree removes an unmounted tree, preserving remaining entries on cancellation.
// Remove leaf entries through pinned directories without following symlinks or
// crossing mounts. Read entries in bounded batches and check cancellation before
// each mutation, including the final removal of the tombstone itself.
func RemoveTree(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("tree cleanup requires a real directory")
	}
	if err := CheckUnmounted(path); err != nil {
		return err
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	root := os.NewFile(uintptr(fd), "tree-cleanup-root")
	defer root.Close()
	if err := removeTreeEntries(ctx, root); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Remove(path)
}

func removeTreeEntries(ctx context.Context, directory *os.File) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, readErr := directory.ReadDir(128)
		if readErr != nil && readErr != io.EOF {
			return readErr
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			flags := 0
			if entry.IsDir() {
				fd, err := unix.Openat2(int(directory.Fd()), entry.Name(), &unix.OpenHow{
					Flags:   unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC,
					Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_XDEV,
				})
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				if err != nil {
					return fmt.Errorf("open tree cleanup directory %s without links or mounts: %w", entry.Name(), err)
				}
				child := os.NewFile(uintptr(fd), "tree-cleanup-directory")
				err = removeTreeEntries(ctx, child)
				closeErr := child.Close()
				if err != nil || closeErr != nil {
					return errors.Join(err, closeErr)
				}
				flags = unix.AT_REMOVEDIR
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := unix.Unlinkat(int(directory.Fd()), entry.Name(), flags); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		if readErr == io.EOF {
			return nil
		}
	}
}
