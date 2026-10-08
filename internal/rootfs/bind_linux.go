//go:build linux

package rootfs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mingo-liu/casklet/internal/config"
	"golang.org/x/sys/unix"
)

// openDirectory walks from the current root using pinned, no-follow descriptors.
// Newly created targets belong only to the private container filesystem.
func openDirectory(path string, create bool) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return nil, errors.New("directory path must be clean and absolute other than /")
	}
	fd, err := unix.Open("/", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	for _, component := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if component == "" || component == "." || component == ".." {
			unix.Close(fd)
			return nil, errors.New("directory path must be clean and absolute")
		}
		next, err := unix.Openat(fd, component, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if create && errors.Is(err, unix.ENOENT) {
			err = unix.Mkdirat(fd, component, 0755)
			if err == nil || errors.Is(err, unix.EEXIST) {
				next, err = unix.Openat(fd, component, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			}
		}
		unix.Close(fd)
		if err != nil {
			return nil, fmt.Errorf("open directory %s without symlinks: %w", path, err)
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), path), nil
}

func openMountSources(mounts []config.BindMount, root string) ([]*os.File, error) {
	if err := config.ValidateMounts(mounts, root); err != nil {
		return nil, err
	}
	files := make([]*os.File, 0, len(mounts))
	for _, mount := range mounts {
		file, err := openDirectory(mount.Source, false)
		if err != nil {
			closeMountSources(files)
			return nil, fmt.Errorf("mount source: %w", err)
		}
		files = append(files, file)
	}
	return files, nil
}

func closeMountSources(files []*os.File) {
	for _, file := range files {
		file.Close()
	}
}

// ValidateMountSources checks sources before allocating a run or detached record.
// Setup opens them again and pins them until all bind mounts have been installed.
func ValidateMountSources(mounts []config.BindMount, root string) error {
	files, err := openMountSources(mounts, root)
	closeMountSources(files)
	return err
}

// mountDirectories runs after pivot_root and runtime mounts, before any workload.
// A nonrecursive bind deliberately excludes mounted descendants of the source.
func mountDirectories(mounts []config.BindMount, sources []*os.File) error {
	for i, mount := range mounts {
		target, err := openDirectory(mount.Target, true)
		if err != nil {
			return fmt.Errorf("mount target: %w", err)
		}
		sourcePath := fmt.Sprintf("/proc/self/fd/%d", sources[i].Fd())
		targetPath := fmt.Sprintf("/proc/self/fd/%d", target.Fd())
		err = unix.Mount(sourcePath, targetPath, "", unix.MS_BIND, "")
		target.Close()
		if err != nil {
			return fmt.Errorf("bind directory at %s: %w", mount.Target, err)
		}
		info, err := os.Open("/proc/self/mountinfo")
		if err != nil {
			return err
		}
		flags, err := bindRemountFlags(info, mount.Target, mount.ReadOnly)
		info.Close()
		if err != nil {
			return err
		}
		// The copied root is private, no workload exists, and targets never overlap.
		// Remount by the validated path so it refers to the new bind, not its old FD.
		if err := unix.Mount("", mount.Target, "", flags, ""); err != nil {
			return fmt.Errorf("restrict bind directory at %s: %w", mount.Target, err)
		}
	}
	return nil
}
