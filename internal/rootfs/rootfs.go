// Package rootfs prepares a private copy of a trusted container filesystem.
package rootfs

import (
	"context"
	"debug/elf"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Validate resolves a trusted template and checks its mount targets and BusyBox.
// The template must remain unchanged until Copy finishes.
func Validate(path string) (string, error) {
	root, err := directory(path)
	if err != nil {
		return "", err
	}
	if root == string(filepath.Separator) {
		return "", errors.New("the host root cannot be used as a rootfs")
	}
	for _, name := range []string{"proc", "dev", "tmp", "bin"} {
		if err := realDirectory(filepath.Join(root, name)); err != nil {
			return "", fmt.Errorf("rootfs %s: %w", name, err)
		}
	}
	busybox := filepath.Join(root, "bin", "busybox")
	info, err := os.Lstat(busybox)
	if err != nil {
		return "", fmt.Errorf("inspect BusyBox: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return "", errors.New("bin/busybox must be a regular executable file")
	}
	f, err := elf.Open(busybox)
	if err != nil {
		return "", fmt.Errorf("BusyBox must be an ELF executable: %w", err)
	}
	defer f.Close()
	machine := map[string]elf.Machine{"amd64": elf.EM_X86_64, "arm64": elf.EM_AARCH64}[runtime.GOARCH]
	if machine == elf.EM_NONE || f.Machine != machine || f.Class != elf.ELFCLASS64 || f.Data != elf.ELFDATA2LSB || (f.Type != elf.ET_EXEC && f.Type != elf.ET_DYN) {
		return "", fmt.Errorf("BusyBox must be a 64-bit Linux ELF executable for %s", runtime.GOARCH)
	}
	if f.OSABI != elf.ELFOSABI_NONE && f.OSABI != elf.ELFOSABI_LINUX {
		return "", fmt.Errorf("BusyBox has an unsupported ELF operating-system ABI: %s", f.OSABI)
	}
	for _, p := range f.Progs {
		if p.Type == elf.PT_INTERP {
			return "", errors.New("BusyBox must be statically linked (ELF interpreter found)")
		}
	}
	libraries, err := f.ImportedLibraries()
	if err != nil {
		return "", fmt.Errorf("inspect BusyBox dependencies: %w", err)
	}
	if len(libraries) != 0 {
		return "", errors.New("BusyBox must be statically linked (shared libraries found)")
	}
	return root, nil
}

func directory(path string) (string, error) {
	if path == "" {
		return "", errors.New("rootfs path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve rootfs: %w", err)
	}
	if err := realDirectory(resolved); err != nil {
		return "", err
	}
	return resolved, nil
}

func realDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s must be a real directory", path)
	}
	return nil
}

func within(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Copy copies a stable, trusted template without traversing symlinks. Destination
// must already exist, be empty, and not overlap source. Errors may leave a partial
// copy, which the caller must remove. Ownership is not copied; special files and
// mounted descendants are rejected, and setuid/setgid bits are never retained.
func Copy(ctx context.Context, source, destination string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	source, err := directory(source)
	if err != nil {
		return err
	}
	if source == string(filepath.Separator) {
		return errors.New("the host root cannot be copied as a rootfs")
	}
	root, err := os.OpenRoot(source)
	if err != nil {
		return err
	}
	defer root.Close()
	return CopyFromRoot(ctx, root, source, destination)
}

// CopyFromRoot confines source access to an already pinned directory. sourcePath
// is its canonical host path, used to reject overlap and mounted descendants.
// The source must remain stable until copying finishes.
func CopyFromRoot(ctx context.Context, root *os.Root, sourcePath, destination string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	source := sourcePath
	if !filepath.IsAbs(source) || filepath.Clean(source) != source || source == "/" {
		return errors.New("invalid pinned rootfs source path")
	}
	if destination == "" {
		return errors.New("rootfs copy destination is required")
	}
	// Lstat follows a final symlink when its path ends in a slash or '/.'.
	// Normalize those suffixes before checking the destination's final entry.
	destination = filepath.Clean(destination)
	if err := realDirectory(destination); err != nil {
		return fmt.Errorf("copy destination: %w", err)
	}
	destination, err := directory(destination)
	if err != nil {
		return err
	}
	if within(source, destination) || within(destination, source) {
		return errors.New("rootfs source and destination must not overlap")
	}
	entries, err := os.ReadDir(destination)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return errors.New("rootfs copy destination must be empty")
	}
	if err := checkMounts(source); err != nil {
		return err
	}
	type copiedDirectory struct {
		path string
		mode fs.FileMode
	}
	var directories []copiedDirectory
	buffer := make([]byte, 32*1024)
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel := name
		target := filepath.Join(destination, rel)
		info, err := root.Lstat(name)
		if err != nil {
			return err
		}
		mode := info.Mode() &^ (os.ModeSetuid | os.ModeSetgid)
		switch {
		case mode.IsDir():
			if rel != "." {
				if err := os.Mkdir(target, 0700); err != nil {
					return err
				}
			}
			directories = append(directories, copiedDirectory{target, mode})
		case mode.IsRegular():
			if err := copyFileFromRoot(ctx, root, name, target, mode, buffer); err != nil {
				return fmt.Errorf("copy %s: %w", rel, err)
			}
		case mode&os.ModeSymlink != 0:
			link, err := root.Readlink(name)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		default:
			return fmt.Errorf("unsupported special file in rootfs: %s", rel)
		}
		return nil
	})
	if err != nil {
		return err
	}
	// Restore directory permissions after children, allowing readonly templates.
	for i := len(directories) - 1; i >= 0; i-- {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := os.Chmod(directories[i].path, directories[i].mode); err != nil {
			return err
		}
	}
	return nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func copyFile(ctx context.Context, source, destination string, mode fs.FileMode, buffer []byte) error {
	in, err := openSource(source)
	if err != nil {
		return err
	}
	defer in.Close()
	return copyOpenedFile(ctx, in, destination, mode, buffer)
}

func copyFileFromRoot(ctx context.Context, root *os.Root, name, destination string, mode fs.FileMode, buffer []byte) error {
	in, err := openRootSource(root, name)
	if err != nil {
		return err
	}
	defer in.Close()
	return copyOpenedFile(ctx, in, destination, mode, buffer)
}

func copyOpenedFile(ctx context.Context, in *os.File, destination string, mode fs.FileMode, buffer []byte) error {
	info, err := in.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("source is no longer a regular file")
	}
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	// Hide File.ReadFrom, which would bypass CopyBuffer's shared buffer. The
	// context reader still checks cancellation before every bounded read.
	_, copyErr := io.CopyBuffer(struct{ io.Writer }{out}, contextReader{ctx: ctx, r: in}, buffer)
	closeErr := out.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return err
	}
	return os.Chmod(destination, mode)
}
