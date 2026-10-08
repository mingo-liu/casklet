package rootfs

import (
	"errors"
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/mingo-liu/casklet/internal/config"
)

// ResolveContainerPath gives symlinks container semantics: absolute targets are relative
// to the image root. os.Root confines every subsequent filesystem operation.
// The staging tree is private and never concurrently modified by another owner.
func ResolveContainerPath(root *os.Root, name string, followFinal bool) (string, error) {
	pending := strings.Split(name, "/")
	var parts []string
	links := 0
	for len(pending) > 0 {
		part := pending[0]
		pending = pending[1:]
		switch part {
		case "", ".":
			continue
		case "..":
			if len(parts) == 0 {
				return "", errors.New("image symlink escapes root")
			}
			parts = parts[:len(parts)-1]
			continue
		}
		candidate := strings.Join(append(append([]string(nil), parts...), part), "/")
		if len(pending) == 0 && !followFinal {
			return candidate, nil
		}
		info, err := root.Lstat(candidate)
		if errors.Is(err, os.ErrNotExist) {
			parts = append(parts, part)
			continue
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink == 0 {
			parts = append(parts, part)
			continue
		}
		links++
		if links > 40 {
			return "", errors.New("too many image symlinks")
		}
		link, err := root.Readlink(candidate)
		if err != nil {
			return "", err
		}
		if path.IsAbs(link) {
			parts = nil
		}
		pending = append(strings.Split(link, "/"), pending...)
	}
	if len(parts) == 0 {
		return ".", nil
	}
	return strings.Join(parts, "/"), nil
}

// PrepareImageWorkdir creates a missing directory before read-only mounts and
// credential reduction, leaving existing image and bind-source ownership alone.
func PrepareImageWorkdir(tree string, cfg config.Config) error {
	root, err := os.OpenRoot(tree)
	if err != nil {
		return err
	}
	defer root.Close()
	name, err := ResolveContainerPath(root, cfg.WorkingDirectory(), true)
	if err != nil {
		return err
	}
	info, err := root.Stat(name)
	if err == nil {
		if !info.IsDir() {
			return fmt.Errorf("image working directory must be a directory")
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := root.MkdirAll(name, 0755); err != nil {
		return err
	}
	if cfg.User != nil {
		return root.Chown(name, int(cfg.User.UID), int(cfg.User.GID))
	}
	return nil
}
