//go:build linux

package rootfs

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// Setup replaces the current root and mounts the runtime filesystems. Call only
// inside a new mount namespace; the caller must discard that namespace on error.
func Setup(path string, readOnly bool) error {
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
	if readOnly {
		mountInfo, err := os.Open("/proc/self/mountinfo")
		if err != nil {
			return fmt.Errorf("inspect rootfs mount flags: %w", err)
		}
		flags, err := readOnlyRootFlags(mountInfo)
		mountInfo.Close()
		if err != nil {
			return fmt.Errorf("inspect rootfs mount flags: %w", err)
		}
		// A bind remount affects only this root mount, keeping the host
		// filesystem and the separate /proc, /dev, and /tmp mounts writable.
		if err := unix.Mount("", "/", "", flags, ""); err != nil {
			return fmt.Errorf("remount rootfs read-only: %w", err)
		}
	}
	return nil
}

// A bind remount replaces per-mount flags, so retain restrictions and timestamp
// behavior inherited from the filesystem containing the private rootfs copy.
func readOnlyRootFlags(mountInfo io.Reader) (uintptr, error) {
	scanner := bufio.NewScanner(mountInfo)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 6 {
			return 0, fmt.Errorf("invalid mountinfo entry: %q", scanner.Text())
		}
		if decodeMountPath(fields[4]) != "/" {
			continue
		}
		flags := uintptr(unix.MS_REMOUNT | unix.MS_BIND | unix.MS_RDONLY | unix.MS_NOSUID | unix.MS_NODEV)
		for _, option := range strings.Split(fields[5], ",") {
			switch option {
			case "noexec":
				flags |= unix.MS_NOEXEC
			case "noatime":
				flags |= unix.MS_NOATIME
			case "nodiratime":
				flags |= unix.MS_NODIRATIME
			case "relatime":
				flags |= unix.MS_RELATIME
			case "strictatime":
				flags |= unix.MS_STRICTATIME
			case "nosymfollow":
				flags |= unix.MS_NOSYMFOLLOW
			}
		}
		return flags, nil
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	return 0, fmt.Errorf("root mount not found")
}
