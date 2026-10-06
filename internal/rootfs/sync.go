package rootfs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

// SyncTree flushes a stable private tree without traversing copied symlinks.
func SyncTree(ctx context.Context, path string) error {
	return filepath.WalkDir(path, func(name string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		file, err := os.Open(name)
		if err != nil {
			return err
		}
		err = file.Sync()
		return errors.Join(err, file.Close())
	})
}
