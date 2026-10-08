//go:build linux

package volume

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/mingo-liu/casklet/internal/config"
	"github.com/mingo-liu/casklet/internal/rootfs"
	"golang.org/x/sys/unix"
)

const maxArchiveBytes int64 = 16 << 30
const maxArchiveEntries = 1000000

type archiveReader struct {
	ctx    context.Context
	reader io.Reader
	count  int64
	tail   [1024]byte
}

func (r *archiveReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	if r.count >= maxArchiveBytes {
		return 0, errors.New("volume archive exceeds 16 GiB")
	}
	if int64(len(p)) > maxArchiveBytes-r.count {
		p = p[:maxArchiveBytes-r.count]
	}
	n, err := r.reader.Read(p)
	if n >= len(r.tail) {
		copy(r.tail[:], p[n-len(r.tail):n])
	} else {
		copy(r.tail[:], r.tail[n:])
		copy(r.tail[len(r.tail)-n:], p[:n])
	}
	r.count += int64(n)
	return n, err
}

type archiveWriter struct {
	ctx    context.Context
	writer io.Writer
	count  int64
}

func (w *archiveWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	if int64(len(p)) > maxArchiveBytes-w.count {
		return 0, errors.New("volume archive exceeds 16 GiB")
	}
	n, err := w.writer.Write(p)
	w.count += int64(n)
	return n, err
}

// Export holds an exclusive usage lease, allowing retained references but no
// live mounts. Callers must quiesce applications before exporting their data.
func (s *Store) Export(ctx context.Context, name string, destination io.Writer) error {
	lock, err := s.lock(ctx)
	if err != nil {
		return err
	}
	lease, root, err := func() (*os.File, *os.Root, error) {
		if _, err := s.read(name); err != nil {
			return nil, nil, err
		}
		lease, err := s.openLock(filepath.Join(s.root, name, ".lease"))
		if err != nil {
			return nil, nil, err
		}
		if err := unix.Flock(int(lease.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
			lease.Close()
			return nil, nil, ErrInUse
		}
		data := filepath.Join(s.root, name, "data")
		if err := rootfs.CheckUnmounted(data); err != nil {
			lease.Close()
			return nil, nil, err
		}
		root, err := os.OpenRoot(data)
		if err != nil {
			lease.Close()
			return nil, nil, err
		}
		return lease, root, nil
	}()
	lock.Close()
	if err != nil {
		return err
	}
	defer lease.Close()
	defer root.Close()
	writer := tar.NewWriter(&archiveWriter{ctx: ctx, writer: destination})
	links := map[[2]uint64]string{}
	count := 0
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		count++
		if count > maxArchiveEntries {
			return errors.New("volume archive exceeds entry limit")
		}
		info, err := root.Lstat(name)
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("volume contains unsupported special file %s", name)
		}
		target := ""
		if info.Mode()&os.ModeSymlink != 0 {
			target, err = root.Readlink(name)
			if err != nil {
				return err
			}
		}
		header, err := tar.FileInfoHeader(info, target)
		if err != nil {
			return err
		}
		header.Name = name
		st := info.Sys().(*syscall.Stat_t)
		header.Uid, header.Gid = int(st.Uid), int(st.Gid)
		header.Uname, header.Gname = "", ""
		if info.Mode().IsRegular() {
			key := [2]uint64{uint64(st.Dev), st.Ino}
			if previous, ok := links[key]; ok {
				header.Typeflag = tar.TypeLink
				header.Linkname = previous
				header.Size = 0
			} else {
				links[key] = name
			}
		}
		if err := writer.WriteHeader(header); err != nil {
			return err
		}
		if header.Typeflag == tar.TypeReg {
			f, err := root.Open(name)
			if err != nil {
				return err
			}
			_, copyErr := io.CopyN(writer, f, header.Size)
			closeErr := f.Close()
			return errors.Join(copyErr, closeErr)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return writer.Close()
}

// Restore publishes only a new name, after complete extraction and fsync.
func (s *Store) Restore(ctx context.Context, name string, source io.Reader) (Record, error) {
	if err := config.ValidateVolumeName(name); err != nil {
		return Record{}, err
	}
	lock, err := s.lock(ctx)
	if err != nil {
		return Record{}, err
	}
	stage, lease, err := func() (string, *os.File, error) {
		if err := s.recover(ctx); err != nil {
			return "", nil, err
		}
		if _, err := os.Lstat(filepath.Join(s.root, name)); !errors.Is(err, os.ErrNotExist) {
			return "", nil, errors.New("restore requires a new volume name")
		}
		stage, err := os.MkdirTemp(s.root, ".create-")
		if err != nil {
			return "", nil, err
		}
		lease, err := s.openLock(filepath.Join(stage, ".lease"))
		if err == nil {
			err = unix.Flock(int(lease.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		}
		if err != nil {
			if lease != nil {
				lease.Close()
			}
			os.RemoveAll(stage)
			return "", nil, err
		}
		return stage, lease, nil
	}()
	lock.Close()
	if err != nil {
		return Record{}, err
	}
	defer lease.Close()
	published := false
	defer func() {
		if published {
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		lock, err := s.lock(cleanup)
		if err != nil {
			return
		}
		defer lock.Close()
		if rootfs.CheckUnmounted(stage) == nil {
			os.RemoveAll(stage)
		}
	}()
	data := filepath.Join(stage, "data")
	if err := os.Mkdir(data, 0755); err != nil {
		return Record{}, err
	}
	root, err := os.OpenRoot(data)
	if err != nil {
		return Record{}, err
	}
	err = restoreArchive(ctx, root, source)
	root.Close()
	if err != nil {
		return Record{}, err
	}
	record := Record{Name: name, CreatedAt: time.Now().UTC()}
	metadata, _ := json.Marshal(record)
	if err := os.WriteFile(filepath.Join(stage, "volume.json"), metadata, 0600); err != nil {
		return Record{}, err
	}
	f, err := os.Open(filepath.Join(stage, "volume.json"))
	if err != nil {
		return Record{}, err
	}
	err = errors.Join(f.Sync(), f.Close())
	if err != nil {
		return Record{}, err
	}
	if err := syncDir(stage); err != nil {
		return Record{}, err
	}
	lock, err = s.lock(ctx)
	if err != nil {
		return Record{}, err
	}
	defer lock.Close()
	if _, err := os.Lstat(filepath.Join(s.root, name)); !errors.Is(err, os.ErrNotExist) {
		return Record{}, errors.New("restore requires a new volume name")
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	if err := os.Rename(stage, filepath.Join(s.root, name)); err != nil {
		return Record{}, err
	}
	published = true
	return record, syncDir(s.root)
}

func safeArchivePath(name string) (string, error) {
	if name == "" || path.IsAbs(name) || len(name) > 4096 || strings.ContainsRune(name, 0) {
		return "", errors.New("invalid volume archive path")
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return "", errors.New("volume archive path contains traversal")
		}
	}
	return path.Clean(name), nil
}

// No archive entry may write through a symlink, even a confined one.
func archiveParents(root *os.Root, name string) error {
	parts := strings.Split(path.Dir(name), "/")
	parent := "."
	for _, part := range parts {
		parent = path.Join(parent, part)
		info, err := root.Lstat(parent)
		if errors.Is(err, os.ErrNotExist) {
			if err := root.Mkdir(parent, 0755); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return errors.New("archive parent must be a directory, not a symlink")
		}
	}
	return nil
}

func archiveMetadata(root *os.Root, h *tar.Header) error {
	if err := root.Lchown(h.Name, h.Uid, h.Gid); err != nil {
		return err
	}
	if h.Typeflag != tar.TypeSymlink {
		mode := fs.FileMode(h.Mode & 0777)
		if h.Mode&01000 != 0 {
			mode |= os.ModeSticky
		}
		if h.Mode&04000 != 0 {
			mode |= os.ModeSetuid
		}
		if h.Mode&02000 != 0 {
			mode |= os.ModeSetgid
		}
		if err := root.Chmod(h.Name, mode); err != nil {
			return err
		}
		if err := root.Chtimes(h.Name, h.ModTime, h.ModTime); err != nil {
			return err
		}
	}
	return nil
}

func restoreArchive(ctx context.Context, root *os.Root, source io.Reader) error {
	stream := &archiveReader{ctx: ctx, reader: source}
	reader := tar.NewReader(stream)
	seen := map[string]bool{}
	var dirs, links []*tar.Header
	for count := 0; ; count++ {
		before := stream.count
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			padding := (512 - before%512) % 512
			if stream.count-before != padding+1024 {
				return errors.New("volume archive is missing its tar terminator")
			}
			for _, b := range stream.tail {
				if b != 0 {
					return errors.New("invalid tar terminator")
				}
			}
			// Accept conventional tar block padding, but reject concatenated archives.
			buffer := make([]byte, 32*1024)
			for {
				n, err := stream.Read(buffer)
				for _, b := range buffer[:n] {
					if b != 0 {
						return errors.New("nonzero trailing archive data")
					}
				}
				if err == io.EOF {
					break
				}
				if err != nil {
					return err
				}
			}
			break
		}
		if err != nil {
			return err
		}
		if count >= maxArchiveEntries {
			return errors.New("volume archive exceeds entry limit")
		}
		name, err := safeArchivePath(header.Name)
		if err != nil {
			return err
		}
		header.Name = name
		if seen[name] {
			return errors.New("volume archive contains duplicate paths")
		}
		seen[name] = true
		if header.Uid < 0 || header.Gid < 0 || uint64(header.Uid) >= uint64(^uint32(0)) || uint64(header.Gid) >= uint64(^uint32(0)) {
			return errors.New("invalid volume archive ownership")
		}
		if header.Size < 0 || header.Size > maxArchiveBytes-stream.count {
			return errors.New("volume archive exceeds size limit")
		}
		if err := archiveParents(root, name); err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := root.MkdirAll(name, 0755); err != nil {
				return err
			}
			dirs = append(dirs, header)
			continue
		case tar.TypeReg, tar.TypeRegA:
			file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(file, reader)
			metadataErr := archiveMetadata(root, header)
			syncErr := file.Sync()
			closeErr := file.Close()
			if err := errors.Join(copyErr, metadataErr, syncErr, closeErr); err != nil {
				return err
			}
			continue
		case tar.TypeSymlink:
			if header.Linkname == "" || strings.ContainsRune(header.Linkname, 0) {
				return errors.New("invalid symlink target")
			}
			if err := root.Symlink(header.Linkname, name); err != nil {
				return err
			}
		case tar.TypeLink:
			target, err := safeArchivePath(header.Linkname)
			if err != nil {
				return err
			}
			header.Linkname = target
			links = append(links, header)
			continue
		default:
			return errors.New("volume archive contains unsupported entry type")
		}
		if err := archiveMetadata(root, header); err != nil {
			return err
		}
	}
	for len(links) > 0 {
		var pending []*tar.Header
		for _, h := range links {
			// Both ancestors and final target must not be symlinks.
			if err := archiveParents(root, h.Linkname); err != nil {
				return err
			}
			info, err := root.Lstat(h.Linkname)
			if errors.Is(err, os.ErrNotExist) {
				pending = append(pending, h)
				continue
			}
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return errors.New("hardlink target must be a regular file")
			}
			if err := root.Link(h.Linkname, h.Name); err != nil {
				return err
			}
		}
		if len(pending) == len(links) {
			return errors.New("volume archive contains unresolved hardlinks")
		}
		links = pending
	}
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i].Name) > len(dirs[j].Name) })
	for _, h := range dirs {
		if err := archiveMetadata(root, h); err != nil {
			return err
		}
	}
	// Sync all directories after metadata and links, deepest first.
	var directories []string
	err := fs.WalkDir(root.FS(), ".", func(name string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() {
			directories = append(directories, name)
		}
		return ctx.Err()
	})
	if err != nil {
		return err
	}
	for i := len(directories) - 1; i >= 0; i-- {
		f, err := root.Open(directories[i])
		if err != nil {
			return err
		}
		err = errors.Join(f.Sync(), f.Close())
		if err != nil {
			return err
		}
	}
	return ctx.Err()
}
