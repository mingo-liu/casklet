//go:build linux

package rootfs

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// Setup replaces the current root and mounts the runtime filesystems. Call only
// inside a new mount namespace; the caller must discard that namespace on error.
func Setup(path string) error {
	root, err := directory(path)
	if err != nil {
		return err
	}
	if root == "/" {
		return fmt.Errorf("cannot switch to the host root")
	}
	for _, name := range []string{"proc", "dev", "tmp"} {
		if err := realDirectory(filepath.Join(root, name)); err != nil {
			return err
		}
	}
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return fmt.Errorf("make mounts private: %w", err)
	}
	if err := unix.Mount(root, root, "", unix.MS_BIND, ""); err != nil {
		return fmt.Errorf("bind rootfs: %w", err)
	}
	oldRoot, err := os.MkdirTemp(root, ".old-root-")
	if err != nil {
		return err
	}
	if err := unix.PivotRoot(root, oldRoot); err != nil {
		return fmt.Errorf("pivot rootfs: %w", err)
	}
	if err := os.Chdir("/"); err != nil {
		return err
	}
	oldRoot = "/" + filepath.Base(oldRoot)
	if err := unix.Unmount(oldRoot, unix.MNT_DETACH); err != nil {
		return fmt.Errorf("unmount old root: %w", err)
	}
	if err := os.Remove(oldRoot); err != nil {
		return fmt.Errorf("remove old root entry: %w", err)
	}
	if err := unix.Mount("proc", "/proc", "proc", unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, ""); err != nil {
		return fmt.Errorf("mount proc: %w", err)
	}
	if err := unix.Mount("tmpfs", "/dev", "tmpfs", unix.MS_NOSUID|unix.MS_NOEXEC, "size=1m,mode=0755"); err != nil {
		return fmt.Errorf("mount dev: %w", err)
	}
	for _, node := range []struct {
		name  string
		minor uint32
	}{{"null", 3}, {"zero", 5}, {"random", 8}, {"urandom", 9}} {
		path := "/dev/" + node.name
		if err := unix.Mknod(path, unix.S_IFCHR|0666, int(unix.Mkdev(1, node.minor))); err != nil {
			return fmt.Errorf("create %s: %w", path, err)
		}
		if err := os.Chmod(path, 0666); err != nil {
			return err
		}
	}
	for _, link := range []struct{ name, target string }{{"fd", "/proc/self/fd"}, {"stdin", "fd/0"}, {"stdout", "fd/1"}, {"stderr", "fd/2"}} {
		if err := os.Symlink(link.target, "/dev/"+link.name); err != nil {
			return err
		}
	}
	if err := unix.Mount("tmpfs", "/tmp", "tmpfs", unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, "size=16m,mode=1777"); err != nil {
		return fmt.Errorf("mount tmp: %w", err)
	}
	return nil
}
