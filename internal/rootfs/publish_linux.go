//go:build linux

package rootfs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// Publish replaces a managed template without an interval where its path is
// missing. The caller holds its exclusive source lease. Both trees must be on
// the same filesystem; an exchanged old tree remains at staged for cleanup.
func Publish(ctx context.Context, staged, destination string) error {
	staged, destination = filepath.Clean(staged), filepath.Clean(destination)
	if !filepath.IsAbs(staged) || !filepath.IsAbs(destination) || within(staged, destination) || within(destination, staged) {
		return errors.New("template publication requires separate absolute paths")
	}
	if err := realDirectory(staged); err != nil {
		return fmt.Errorf("staged template: %w", err)
	}
	if _, err := Validate(staged); err != nil {
		return err
	}
	flags := uint(unix.RENAME_NOREPLACE)
	if info, err := os.Lstat(destination); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("managed template is not a real directory: %s", destination)
		}
		if err := CheckUnmounted(destination); err != nil {
			return err
		}
		flags = unix.RENAME_EXCHANGE
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := CheckUnmounted(staged); err != nil {
		return err
	}
	if err := SyncTree(ctx, staged); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := unix.Renameat2(unix.AT_FDCWD, staged, unix.AT_FDCWD, destination, flags); err != nil {
		return fmt.Errorf("publish managed template: %w", err)
	}
	// Persist both sides of an exchange before the caller removes the old tree.
	for _, dir := range []string{filepath.Dir(destination), filepath.Dir(staged)} {
		file, err := os.Open(dir)
		if err != nil {
			return err
		}
		if err := errors.Join(file.Sync(), file.Close()); err != nil {
			return err
		}
	}
	return nil
}
