//go:build linux

package storage

import (
	"context"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"
)

// AllocatedBytes counts allocated blocks once per inode, including directories.
// Openat2 pins each directory, rejects symlinks and excludes mounted descendants.
// Concurrent deletions are skipped; other errors prevent misleading zero totals.
func AllocatedBytes(ctx context.Context, path string) (uint64, error) {
	fd, err := unix.Openat2(unix.AT_FDCWD, path, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS})
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	seen := map[[2]uint64]bool{}
	return walk(ctx, f, seen, 0, "")
}
func walk(ctx context.Context, dir *os.File, seen map[[2]uint64]bool, depth int, exclude string) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if depth > 1024 {
		return 0, errors.New("storage tree exceeds scan depth limit")
	}
	var st unix.Stat_t
	if err := unix.Fstat(int(dir.Fd()), &st); err != nil {
		return 0, err
	}
	key := [2]uint64{uint64(st.Dev), st.Ino}
	if seen[key] {
		return 0, nil
	}
	seen[key] = true
	total := uint64(st.Blocks) * 512
	for {
		entries, err := dir.ReadDir(256)
		if err != nil && !errors.Is(err, io.EOF) {
			return 0, err
		}
		for _, entry := range entries {
			if entry.Name() == exclude {
				continue
			}
			if err := ctx.Err(); err != nil {
				return 0, err
			}
			var child unix.Stat_t
			err := unix.Fstatat(int(dir.Fd()), entry.Name(), &child, unix.AT_SYMLINK_NOFOLLOW)
			if errors.Is(err, unix.ENOENT) {
				continue
			}
			if err != nil {
				return 0, err
			}
			if child.Mode&unix.S_IFMT == unix.S_IFDIR {
				fd, err := unix.Openat2(int(dir.Fd()), entry.Name(), &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_XDEV})
				if errors.Is(err, unix.EXDEV) || errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
					continue
				}
				if err != nil {
					return 0, err
				}
				f := os.NewFile(uintptr(fd), entry.Name())
				n, err := walk(ctx, f, seen, depth+1, "")
				f.Close()
				if err != nil {
					return 0, err
				}
				total += n
			} else {
				fd, err := unix.Openat2(int(dir.Fd()), entry.Name(), &unix.OpenHow{Flags: unix.O_PATH | unix.O_NOFOLLOW | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_XDEV})
				if errors.Is(err, unix.EXDEV) || errors.Is(err, unix.ENOENT) {
					continue
				}
				if err != nil {
					return 0, err
				}
				err = unix.Fstat(fd, &child)
				unix.Close(fd)
				if err != nil {
					return 0, err
				}
				key := [2]uint64{uint64(child.Dev), child.Ino}
				if !seen[key] {
					seen[key] = true
					total += uint64(child.Blocks) * 512
				}
			}
		}
		if errors.Is(err, io.EOF) {
			return total, nil
		}
	}
}
func filesystem(path string) (Filesystem, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return Filesystem{}, err
	}
	r := Filesystem{TotalBytes: st.Blocks * uint64(st.Bsize), FreeBytes: st.Bfree * uint64(st.Bsize), AvailableBytes: st.Bavail * uint64(st.Bsize)}
	r.LowSpace = r.AvailableBytes < 1<<30 || r.AvailableBytes < r.TotalBytes/10
	return r, nil
}
func DiskUsage(ctx context.Context) (Report, error) { return usageAt(ctx, "/var/lib/casklet") }

// Partition a pinned image tree without double counting or entering a mounted
// .blobs directory. Shared seen-inode state retains hardlink accounting.
func imageAllocation(ctx context.Context, path string) (uint64, uint64, error) {
	fd, err := unix.Openat2(unix.AT_FDCWD, path, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS})
	if errors.Is(err, os.ErrNotExist) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, err
	}
	dir := os.NewFile(uintptr(fd), path)
	defer dir.Close()
	seen := map[[2]uint64]bool{}
	images, err := walk(ctx, dir, seen, 0, ".blobs")
	if err != nil {
		return 0, 0, err
	}
	cacheFD, err := unix.Openat2(fd, ".blobs", &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_XDEV})
	if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.EXDEV) {
		return images, 0, nil
	}
	if err != nil {
		return 0, 0, err
	}
	cache := os.NewFile(uintptr(cacheFD), ".blobs")
	defer cache.Close()
	blobs, err := walk(ctx, cache, seen, 0, "")
	return images, blobs, err
}
func usageAt(ctx context.Context, root string) (Report, error) {
	if _, err := os.Stat(root); err != nil {
		return Report{}, err
	}
	fs, err := filesystem(root)
	if err != nil {
		return Report{}, err
	}
	r := Report{Filesystem: fs, Categories: []Category{}}
	images, cache, err := imageAllocation(ctx, filepath.Join(root, "images"))
	if err != nil {
		return Report{}, fmt.Errorf("measure image storage: %w", err)
	}
	r.Categories = append(r.Categories, Category{"images", images}, Category{"image-cache", cache})
	for _, kind := range []string{"containers", "volumes", "runs", "templates"} {
		n, err := AllocatedBytes(ctx, filepath.Join(root, kind))
		if err != nil {
			return Report{}, fmt.Errorf("measure %s: %w", kind, err)
		}
		r.Categories = append(r.Categories, Category{kind, n})
	}
	return r, nil
}
