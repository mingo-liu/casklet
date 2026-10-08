package image

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"

	"github.com/mingo-liu/casklet/internal/rootfs"
)

const maxLayerBytes int64 = 4 << 30
const maxImageBytes int64 = 16 << 30
const maxLayerEntries = 1000000

func archivePath(name string) (string, error) {
	if name == "" || strings.HasPrefix(name, "/") || strings.ContainsRune(name, 0) {
		return "", fmt.Errorf("unsafe image path %q", name)
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return "", fmt.Errorf("unsafe image path %q", name)
		}
	}
	return path.Clean(name), nil
}

func resolvePath(root *os.Root, name string, followFinal bool) (string, error) {
	return rootfs.ResolveContainerPath(root, name, followFinal)
}

func walkLayer(ctx context.Context, archive *os.File, visit func(*tar.Header, *tar.Reader) error, progress ...func(int)) error {
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return err
	}
	var source io.Reader = contextReader{ctx: ctx, r: archive}
	if len(progress) > 0 {
		source = countingReader{reader: source, read: progress[0]}
	}
	reader := tar.NewReader(source)
	for count := 0; ; count++ {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if count >= maxLayerEntries {
			return errors.New("image layer exceeds entry limit")
		}
		name, err := archivePath(header.Name)
		if err != nil {
			return err
		}
		header.Name = name
		if err := visit(header, reader); err != nil {
			return fmt.Errorf("unpack %s: %w", name, err)
		}
	}
}

// applyLayer applies all whiteouts before additions, so opaque markers cannot
// delete additions from their own layer, regardless of archive entry order.
func applyLayer(ctx context.Context, root *os.Root, archive *os.File, progress ...func(int)) error {
	err := walkLayer(ctx, archive, func(h *tar.Header, _ *tar.Reader) error {
		base := path.Base(h.Name)
		if !strings.HasPrefix(base, ".wh.") {
			return nil
		}
		if h.Typeflag != tar.TypeReg || h.Size != 0 {
			return errors.New("whiteout must be an empty regular file")
		}
		dir, err := resolvePath(root, path.Dir(h.Name), true)
		if err != nil {
			return err
		}
		if base == ".wh..wh..opq" {
			f, err := root.Open(dir)
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			entries, readErr := f.ReadDir(-1)
			f.Close()
			if readErr != nil {
				return readErr
			}
			for _, entry := range entries {
				if err := root.RemoveAll(path.Join(dir, entry.Name())); err != nil {
					return err
				}
			}
			return nil
		}
		target := strings.TrimPrefix(base, ".wh.")
		if target == "" || target == "." || target == ".." {
			return errors.New("invalid whiteout target")
		}
		return root.RemoveAll(path.Join(dir, target))
	}, progress...)
	if err != nil {
		return err
	}
	type metadata struct {
		name     string
		uid, gid int
		mode     fs.FileMode
	}
	var directories []metadata
	seen := map[string]bool{}
	type hardlink struct{ name, target string }
	var links []hardlink
	err = walkLayer(ctx, archive, func(h *tar.Header, reader *tar.Reader) error {
		if strings.HasPrefix(path.Base(h.Name), ".wh.") {
			return nil
		}
		if h.Uid < 0 || h.Gid < 0 || uint64(h.Uid) >= uint64(^uint32(0)) || uint64(h.Gid) >= uint64(^uint32(0)) {
			return errors.New("invalid image ownership")
		}
		name, err := resolvePath(root, h.Name, false)
		if err != nil {
			return err
		}
		if name == "." && h.Typeflag != tar.TypeDir {
			return errors.New("image root must be a directory")
		}
		if seen[name] {
			return errors.New("image layer contains duplicate paths")
		}
		seen[name] = true
		if err := root.MkdirAll(path.Dir(name), 0755); err != nil {
			return err
		}
		mode := fs.FileMode(h.Mode & 0777)
		if h.Mode&01000 != 0 {
			mode |= os.ModeSticky
		}
		if h.Typeflag == tar.TypeDir {
			info, err := root.Lstat(name)
			if err == nil && !info.IsDir() {
				if err := root.RemoveAll(name); err != nil {
					return err
				}
			}
			if err := root.MkdirAll(name, 0755); err != nil {
				return err
			}
			directories = append(directories, metadata{name, h.Uid, h.Gid, mode})
			return nil
		}
		if err := root.RemoveAll(name); err != nil {
			return err
		}
		switch h.Typeflag {
		case tar.TypeReg, tar.TypeRegA:
			f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(f, contextReader{ctx: ctx, r: reader})
			if err := errors.Join(copyErr, f.Close()); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if h.Linkname == "" || strings.ContainsRune(h.Linkname, 0) {
				return errors.New("invalid symlink target")
			}
			if !path.IsAbs(h.Linkname) {
				target := path.Clean(path.Join(path.Dir(h.Name), h.Linkname))
				if target == ".." || strings.HasPrefix(target, "../") {
					return errors.New("symlink escapes image root")
				}
			}
			if err := root.Symlink(h.Linkname, name); err != nil {
				return err
			}
		case tar.TypeLink:
			target, err := archivePath(h.Linkname)
			if err != nil {
				return err
			}
			resolved, err := resolvePath(root, target, true)
			if err != nil {
				return err
			}
			info, err := root.Lstat(resolved)
			if errors.Is(err, os.ErrNotExist) {
				links = append(links, hardlink{name, target})
				return nil
			}
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return errors.New("hardlink target must be a regular image file")
			}
			return root.Link(resolved, name)
		default:
			return fmt.Errorf("unsupported image entry type %d (devices, FIFOs, and sockets are not supported)", h.Typeflag)
		}
		if err := setOwnership(root, name, h.Uid, h.Gid); err != nil {
			return err
		}
		if h.Typeflag != tar.TypeSymlink {
			return root.Chmod(name, mode)
		}
		return nil
	}, progress...)
	if err != nil {
		return err
	}
	for len(links) > 0 {
		var pending []hardlink
		for _, link := range links {
			target, err := resolvePath(root, link.target, true)
			if err != nil {
				return err
			}
			info, err := root.Lstat(target)
			if errors.Is(err, os.ErrNotExist) {
				pending = append(pending, link)
				continue
			}
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return errors.New("hardlink target must be a regular image file")
			}
			if err := root.Link(target, link.name); err != nil {
				return err
			}
		}
		if len(pending) == len(links) {
			return errors.New("image contains unresolved hardlinks")
		}
		links = pending
	}
	for i := len(directories) - 1; i >= 0; i-- {
		dir := directories[i]
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := setOwnership(root, dir.name, dir.uid, dir.gid); err != nil {
			return err
		}
		if err := root.Chmod(dir.name, dir.mode); err != nil {
			return err
		}
	}
	return nil
}

func setOwnership(root *os.Root, name string, uid, gid int) error {
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}
	currentUID, currentGID := rootfs.Ownership(info)
	if currentUID == uint32(uid) && currentGID == uint32(gid) {
		return nil
	}
	return root.Lchown(name, uid, gid)
}
