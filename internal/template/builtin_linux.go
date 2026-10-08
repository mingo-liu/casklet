//go:build linux

package template

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/mingo-liu/mini-docker/internal/rootfs"
	"golang.org/x/sys/unix"
)

const builtinStagePrefix = ".busybox-stage-"

type builtinMetadata struct {
	Architecture string   `json:"architecture"`
	Package      string   `json:"package"`
	Version      string   `json:"version"`
	SHA256       string   `json:"sha256"`
	Applets      []string `json:"applets"`
}

func checkBuiltinDirectory(path string, owner uint32) error {
	var stat unix.Stat_t
	if err := unix.Lstat(path, &stat); err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Uid != owner || stat.Mode&0022 != 0 {
		return fmt.Errorf("unsafe builtin template directory: %s (mode %04o, UID %d; expected UID %d)", path, stat.Mode&07777, stat.Uid, owner)
	}
	return nil
}

func lockBuiltin(ctx context.Context, exclusive bool) (io.Closer, error) {
	lease, err := lockBuiltinAt(ctx, filepath.Dir(BuiltinPath), 0, exclusive)
	if err != nil {
		return nil, err
	}
	return lease, nil
}

// A stable lock outside the exchanged tree is readable by rootless users.
// Only the guest installer can create it or acquire it for mutation.
func lockBuiltinAt(ctx context.Context, parent string, owner uint32, exclusive bool) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := checkBuiltinDirectory(parent, owner); err != nil {
		return nil, err
	}
	path := filepath.Join(parent, ".busybox.lock")
	flags, operation := unix.O_RDONLY, unix.LOCK_SH
	if exclusive {
		flags, operation = unix.O_RDWR, unix.LOCK_EX
		fd, err := unix.Open(path, flags|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0644)
		if err == nil {
			err = unix.Fchmod(fd, 0644)
			closeErr := unix.Close(fd)
			if err := errors.Join(err, closeErr); err != nil {
				return nil, err
			}
		} else if !errors.Is(err, unix.EEXIST) {
			return nil, err
		}
	}
	fd, err := unix.Open(path, flags|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		file.Close()
		return nil, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Uid != owner || stat.Mode&0777 != 0644 || stat.Nlink != 1 {
		file.Close()
		return nil, errors.New("unsafe builtin template lease")
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			file.Close()
			return nil, err
		}
		err := unix.Flock(fd, operation|unix.LOCK_NB)
		if err == nil {
			return file, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EINTR) {
			file.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			file.Close()
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

// EnsureBuiltin validates or repairs the product's fixed builtin template.
// Preparation runs in a private sibling tree. Failure preserves the old source;
// publication and removal wait until every active source copy releases its lease.
func EnsureBuiltin(ctx context.Context, prepare func(context.Context, string) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if os.Geteuid() != 0 {
		return errors.New("builtin template repair requires guest root privileges")
	}
	for _, dir := range []string{"/var", "/var/lib", "/var/lib/mini-docker", filepath.Dir(BuiltinPath)} {
		if err := os.Mkdir(dir, 0755); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		if err := checkBuiltinDirectory(dir, 0); err != nil {
			return err
		}
	}
	return ensureBuiltinAt(ctx, BuiltinPath, 0, prepare)
}

func ensureBuiltinAt(ctx context.Context, path string, owner uint32, prepare func(context.Context, string) error) (result error) {
	parent := filepath.Dir(path)
	lease, err := lockBuiltinAt(ctx, parent, owner, true)
	if err != nil {
		return err
	}
	defer lease.Close()
	entries, err := os.ReadDir(parent)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), builtinStagePrefix) {
			if err := removeBuiltinStage(filepath.Join(parent, entry.Name()), owner); err != nil {
				return err
			}
		}
	}
	if err := validateBuiltin(ctx, path, owner); err == nil {
		return nil
	}
	// Never replace a symlink, mounted tree, or publicly writable directory.
	if err := checkBuiltinDirectory(path, owner); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := rootfs.CheckUnmounted(path); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(parent, builtinStagePrefix)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, removeBuiltinStage(stage, owner)) }()
	candidate := filepath.Join(stage, "rootfs")
	if err := prepare(ctx, candidate); err != nil {
		return fmt.Errorf("prepare builtin template: %w", err)
	}
	if err := validateBuiltin(ctx, candidate, owner); err != nil {
		return fmt.Errorf("validate prepared builtin template: %w", err)
	}
	return rootfs.Publish(ctx, candidate, path)
}

func removeBuiltinStage(path string, owner uint32) error {
	if err := checkBuiltinDirectory(path, owner); err != nil {
		return err
	}
	if err := rootfs.CheckUnmounted(path); err != nil {
		return err
	}
	return os.RemoveAll(path)
}

func validateBuiltin(ctx context.Context, path string, owner uint32) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := checkBuiltinDirectory(path, owner); err != nil {
		return err
	}
	if _, err := rootfs.ValidateBusyBox(path); err != nil {
		return err
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return err
	}
	defer root.Close()
	metadata, err := root.OpenFile(".mini-docker-rootfs.json", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	data, readErr := io.ReadAll(io.LimitReader(metadata, 64*1024+1))
	if err := errors.Join(readErr, metadata.Close()); err != nil {
		return err
	}
	var record builtinMetadata
	if len(data) > 64*1024 || json.Unmarshal(data, &record) != nil || record.Architecture != runtime.GOARCH || record.Package != "busybox-static" || record.Version == "" || len(record.Applets) == 0 || len(record.Applets) > 1024 {
		return errors.New("invalid builtin template metadata")
	}
	expected := map[string]fs.FileMode{
		".": os.ModeDir | 0755, "bin": os.ModeDir | 0755,
		"proc": os.ModeDir | 0755, "dev": os.ModeDir | 0755,
		"tmp": os.ModeDir | os.ModeSticky | 0777, "etc": os.ModeDir | 0755,
		"usr": os.ModeDir | 0755, "usr/bin": os.ModeDir | 0755,
		"bin/busybox": 0755, "etc/passwd": 0644, "etc/group": 0644,
		".mini-docker-rootfs.json": 0644,
	}
	for _, applet := range record.Applets {
		if applet == "" || applet == "." || applet == ".." || applet == "busybox" || strings.ContainsAny(applet, "/\x00") {
			return errors.New("invalid builtin applet name")
		}
		name := "bin/" + applet
		if _, exists := expected[name]; exists {
			return errors.New("duplicate builtin applet")
		}
		expected[name] = os.ModeSymlink | 0777
	}
	if expected["bin/sh"]&os.ModeSymlink == 0 {
		return errors.New("builtin template requires the sh applet")
	}
	if err := fs.WalkDir(root.FS(), ".", func(name string, _ fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := root.Lstat(name)
		if err != nil {
			return err
		}
		mode, exists := expected[name]
		stat := info.Sys().(*syscall.Stat_t)
		if !exists || info.Mode() != mode || stat.Uid != owner || (mode.IsRegular() && stat.Nlink != 1) {
			return fmt.Errorf("invalid builtin template entry: %s", name)
		}
		if mode&os.ModeSymlink != 0 {
			link, err := root.Readlink(name)
			if err != nil || link != "busybox" {
				return fmt.Errorf("invalid builtin applet link: %s", name)
			}
		}
		delete(expected, name)
		return nil
	}); err != nil {
		return err
	}
	if len(expected) != 0 {
		return errors.New("builtin template is incomplete")
	}
	busybox, err := root.OpenFile("bin/busybox", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer busybox.Close()
	info, err := busybox.Stat()
	if err != nil {
		return err
	}
	if info.Size() > 32<<20 {
		return errors.New("builtin BusyBox exceeds its size limit")
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, io.LimitReader(busybox, 32<<20)); err != nil {
		return err
	}
	if record.SHA256 != fmt.Sprintf("%x", digest.Sum(nil)) {
		return errors.New("builtin BusyBox checksum mismatch")
	}
	return ctx.Err()
}
