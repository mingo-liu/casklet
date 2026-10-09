//go:build linux

package container

import (
	"errors"
	"os"

	"github.com/mingo-liu/casklet/internal/config"
	"github.com/mingo-liu/casklet/internal/rootfs"
)

// retainedRootCandidate validates the retained backend before a new execution
// is published. Complete copied roots do not depend on their original source.
// Overlay metadata belongs to a private sibling, outside the container root.
func retainedRootCandidate(cfg config.Config, retained string) (string, bool, error) {
	info, err := os.Lstat(retained)
	if err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", false, errors.New("retained rootfs must be a real directory")
		}
		return retained, false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", false, err
	}
	storage := rootfs.OverlayStoragePath(retained)
	info, err = os.Lstat(storage)
	if errors.Is(err, os.ErrNotExist) {
		return cfg.RootFS, cfg.Image != "", nil
	}
	if err != nil {
		return "", false, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", false, errors.New("retained overlay storage must be a real directory")
	}
	id, overlay, err := rootfs.ReadOverlayImageID(storage)
	if err != nil {
		return "", false, err
	}
	if !overlay || id != cfg.Image || cfg.UserNS || cfg.Rootless {
		return "", false, errors.New("retained overlay rootfs does not match the container image")
	}
	return cfg.RootFS, true, nil
}
