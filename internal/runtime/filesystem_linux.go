//go:build linux

package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mingo-liu/mini-docker/internal/config"
	"github.com/mingo-liu/mini-docker/internal/rootfs"
	"github.com/mingo-liu/mini-docker/internal/template"
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
	}
	source, err := template.Acquire(ctx, cfg)
	return source, false, err
}

// prepareRunRootFS owns filesystem publication only. The run supervisor owns
// transient directory cleanup and the container store owns retained roots.
func prepareRunRootFS(ctx context.Context, source, runPath, retainedRoot string, retainedReady bool) (string, error) {
	if retainedRoot == "" {
		path := filepath.Join(runPath, "rootfs")
		if err := os.Mkdir(path, 0700); err != nil {
			return "", err
		}
		if err := rootfs.Copy(ctx, source, path); err != nil {
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
	if err := rootfs.Copy(ctx, source, stage); err != nil {
		return "", fmt.Errorf("prepare retained rootfs: %w", err)
	}
	if err := rootfs.SyncTree(ctx, stage); err != nil {
		return "", err
	}
	if err := os.Rename(stage, retainedRoot); err != nil {
		return "", err
	}
	parent, err := os.Open(filepath.Dir(retainedRoot))
	if err != nil {
		return "", err
	}
	err = parent.Sync()
	parent.Close()
	if err != nil {
		return "", err
	}
	return retainedRoot, nil
}
