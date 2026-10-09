//go:build linux

package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mingo-liu/casklet/internal/config"
	"github.com/mingo-liu/casklet/internal/rootfs"
	"github.com/mingo-liu/casklet/internal/template"
	"golang.org/x/sys/unix"
)

// A retained root takes precedence over the original source on restart. This
// lets a container restart after its original directory has been removed.
func acquireRunTemplate(ctx context.Context, cfg config.Config, retainedRoot string) (*template.Template, bool, error) {
	if retainedRoot != "" {
		info, err := os.Lstat(retainedRoot)
		if err == nil {
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return nil, false, errors.New("retained rootfs must be a real directory")
			}
			if err := rootfs.CheckUnmounted(retainedRoot); err != nil {
				return nil, false, err
			}
			path, err := rootfs.Validate(retainedRoot)
			if err != nil {
				return nil, false, err
			}
			if err := rootfs.ValidateMountSources(cfg.Mounts, path); err != nil {
				return nil, false, err
			}
			return &template.Template{Path: path}, true, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, false, err
		}
		storage := rootfs.OverlayStoragePath(retainedRoot)
		if _, err := os.Lstat(storage); err == nil {
			id, ready, err := rootfs.ReadOverlayImageID(storage)
			if err != nil {
				return nil, false, err
			}
			if !ready || id != cfg.Image || cfg.UserNS || cfg.Rootless {
				return nil, false, errors.New("retained overlay does not match the configured privileged image")
			}
			source, err := template.Acquire(ctx, cfg)
			return source, true, err
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, false, err
		}
	}
	source, err := template.Acquire(ctx, cfg)
	return source, false, err
}

// prepareImageOverlay publishes only the writable storage. Its merged view is
// mounted later by init, so supervisor death cannot leave a host mount behind.
func prepareImageOverlay(ctx context.Context, source, image, runPath, retainedRoot string, retainedReady bool) (*rootfs.Overlay, error) {
	storage := retainedRoot
	if storage == "" {
		storage = filepath.Join(runPath, "overlay")
		if err := os.Mkdir(storage, 0700); err != nil {
			return nil, err
		}
		if err := rootfs.CreateOverlayStorage(ctx, source, storage, image); err != nil {
			return nil, err
		}
	} else {
		storage = rootfs.OverlayStoragePath(retainedRoot)
	}
	if retainedRoot != "" && !retainedReady {
		stage, err := os.MkdirTemp(filepath.Dir(storage), ".rootfs-")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(stage)
		if err := rootfs.CreateOverlayStorage(ctx, source, stage, image); err != nil {
			return nil, err
		}
		if err := rootfs.SyncTree(ctx, stage); err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// A concurrent or unexpected existing root must never be replaced.
		if err := publishRootFS(stage, storage); err != nil {
			return nil, err
		}
	} else if retainedRoot != "" {
		id, ready, err := rootfs.ReadOverlayImageID(storage)
		if err != nil {
			return nil, err
		}
		if !ready || id != image {
			return nil, errors.New("retained overlay image identity changed")
		}
	}
	target := filepath.Join(runPath, "rootfs")
	if err := os.Mkdir(target, 0700); err != nil {
		return nil, err
	}
	return &rootfs.Overlay{Lower: source, Storage: storage, Target: target}, nil
}

// prepareRunRootFS owns filesystem publication only. The run supervisor owns
// transient directory cleanup and the container store owns retained roots.
func prepareRunRootFS(ctx context.Context, source, runPath, retainedRoot string, retainedReady bool, ownership ...bool) (string, error) {
	copyRoot := rootfs.Copy
	if len(ownership) > 0 && ownership[0] {
		copyRoot = rootfs.CopyOwned
	}
	if retainedRoot == "" {
		path := filepath.Join(runPath, "rootfs")
		if err := os.Mkdir(path, 0700); err != nil {
			return "", err
		}
		if err := copyRoot(ctx, source, path); err != nil {
			return "", fmt.Errorf("prepare rootfs: %w", err)
		}
		return path, nil
	}
	if retainedReady {
		return retainedRoot, nil
	}
	stage, err := os.MkdirTemp(filepath.Dir(retainedRoot), ".rootfs-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)
	if err := copyRoot(ctx, source, stage); err != nil {
		return "", fmt.Errorf("prepare retained rootfs: %w", err)
	}
	if err := rootfs.SyncTree(ctx, stage); err != nil {
		return "", err
	}
	if err := publishRootFS(stage, retainedRoot); err != nil {
		return "", err
	}
	return retainedRoot, nil
}

func publishRootFS(stage, destination string) error {
	if err := unix.Renameat2(unix.AT_FDCWD, stage, unix.AT_FDCWD, destination, unix.RENAME_NOREPLACE); err != nil {
		return err
	}
	parent, err := os.Open(filepath.Dir(destination))
	if err != nil {
		return err
	}
	return errors.Join(parent.Sync(), parent.Close())
}
