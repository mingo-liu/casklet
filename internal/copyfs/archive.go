// Package copyfs transfers one file or directory through a bounded tar stream.
// Archives are private transport, not an additional public import format.
package copyfs

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
)

const MaxBytes int64 = 16 << 30
const maxEntries = 1000000

type stream struct {
	ctx  context.Context
	r    io.Reader
	w    io.Writer
	n    int64
	tail [1024]byte
}

func (s *stream) Read(p []byte) (int, error) {
	if err := s.ctx.Err(); err != nil {
		return 0, err
	}
	if s.n >= MaxBytes {
		var extra [1]byte
		n, err := s.r.Read(extra[:])
		if n == 0 && err == io.EOF {
			return 0, io.EOF
		}
		return 0, errors.New("copy exceeds 16 GiB archive limit")
	}
	if int64(len(p)) > MaxBytes-s.n {
		p = p[:MaxBytes-s.n]
	}
	n, err := s.r.Read(p)
	if n >= len(s.tail) {
		copy(s.tail[:], p[n-len(s.tail):n])
	} else {
		copy(s.tail[:], s.tail[n:])
		copy(s.tail[len(s.tail)-n:], p[:n])
	}
	s.n += int64(n)
	return n, err
}
func (s *stream) Write(p []byte) (int, error) {
	if err := s.ctx.Err(); err != nil {
		return 0, err
	}
	if int64(len(p)) > MaxBytes-s.n {
		return 0, errors.New("copy exceeds 16 GiB archive limit")
	}
	n, err := s.w.Write(p)
	s.n += int64(n)
	return n, err
}

// Export never dereferences the source's final symlink. A trailing /. copies
// directory contents; other paths send the source basename as the top entry.
func Export(ctx context.Context, root *os.Root, source string, destination io.Writer) error {
	contents := strings.HasSuffix(source, "/.")
	source = path.Clean(source)
	parent, err := openParent(root, source)
	if err != nil {
		return err
	}
	defer parent.Close()
	root, source = parent, path.Base(source)
	info, err := root.Lstat(source)
	if err != nil {
		return err
	}
	if contents && !info.IsDir() {
		return errors.New("copy contents requires a directory")
	}
	base := path.Base(source)
	if contents {
		base = "."
	}
	writer := tar.NewWriter(&stream{ctx: ctx, w: destination})
	count := 0
	var visit func(*os.Root, string, string, int) error
	visit = func(root *os.Root, name, archived string, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		count++
		if count > maxEntries || depth >= 256 {
			return errors.New("copy exceeds entry or depth limit")
		}
		info, err := root.Lstat(name)
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("copy rejects special file %s", name)
		}
		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			link, err = root.Readlink(name)
			if err != nil {
				return err
			}
		}
		header, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		header.Name, header.Mode = archived, int64(info.Mode().Perm())
		header.Uid, header.Gid, header.Uname, header.Gname = 0, 0, "", ""
		if err := writer.WriteHeader(header); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			file, err := openRegular(root, name)
			if err != nil {
				return err
			}
			defer file.Close()
			current, err := file.Stat()
			if err != nil || !current.Mode().IsRegular() || !os.SameFile(info, current) {
				return errors.New("copy source changed")
			}
			_, err = io.CopyN(writer, file, info.Size())
			return err
		}
		if info.IsDir() {
			dir, err := root.OpenRoot(name)
			if err != nil {
				return err
			}
			defer dir.Close()
			current, err := dir.Stat(".")
			if err != nil || !os.SameFile(info, current) {
				return errors.New("copy source directory changed")
			}
			f, err := dir.Open(".")
			if err != nil {
				return err
			}
			defer f.Close()
			for {
				entries, err := f.ReadDir(128)
				if err != nil && err != io.EOF {
					return err
				}
				for _, entry := range entries {
					if err := visit(dir, entry.Name(), path.Join(archived, entry.Name()), depth+1); err != nil {
						return err
					}
				}
				if err == io.EOF {
					break
				}
			}
		}
		return nil
	}
	if err := visit(root, source, base, 0); err != nil {
		return err
	}
	return writer.Close()
}

// Import merges directories and replaces individual files by rename. It never
// traverses destination symlinks, preserves permission bits (not ownership or
// special bits), and leaves completed entries intact on failure/cancellation.
func Import(ctx context.Context, root *os.Root, destination string, source io.Reader) error {
	return ImportChecked(ctx, root, destination, source, nil)
}

// ImportChecked applies a receiving-filesystem policy before each entry mutation.
func ImportChecked(ctx context.Context, root *os.Root, destination string, source io.Reader, check func(string) error) error {
	input := &stream{ctx: ctx, r: source}
	reader := tar.NewReader(input)
	first, err := reader.Next()
	if err != nil {
		return fmt.Errorf("read copy header: %w", err)
	}
	top := first.Name
	if top != "." && (!fs.ValidPath(top) || strings.Contains(top, "/")) {
		return errors.New("invalid copy root entry")
	}
	directoryDestination := strings.HasSuffix(destination, "/")
	destination = path.Clean(destination)
	info, statErr := root.Lstat(destination)
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	if directoryDestination && (statErr != nil || !info.IsDir()) {
		return errors.New("copy destination ending in / must be an existing directory")
	}
	if top == "." {
		if first.Typeflag != tar.TypeDir || statErr != nil || !info.IsDir() {
			return errors.New("copy contents requires an existing destination directory")
		}
	} else if statErr == nil && info.IsDir() {
		destination = path.Join(destination, top)
	}
	seen := map[string]bool{}
	var directories []string
	var headers []*tar.Header
	for header, count := first, 0; ; count++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := header.Name
		for key := range header.PAXRecords {
			if strings.HasPrefix(key, "GNU.sparse.") {
				return errors.New("copy rejects sparse archive metadata")
			}
		}
		if count >= maxEntries || len(strings.Split(name, "/")) > 256 || (name != "." && !fs.ValidPath(name)) || seen[name] {
			return errors.New("invalid, duplicate, or excessive copy entries")
		}
		seen[name] = true
		if header.Mode < 0 || header.Mode & ^int64(0777) != 0 || header.Size < 0 || header.Size > MaxBytes || header.Typeflag != tar.TypeReg && header.Size != 0 {
			return errors.New("unsupported copy metadata")
		}
		relative := "."
		if name != top {
			if top == "." {
				relative = name
			} else if strings.HasPrefix(name, top+"/") {
				relative = strings.TrimPrefix(name, top+"/")
			} else {
				return errors.New("copy entry outside source tree")
			}
			if first.Typeflag != tar.TypeDir {
				return errors.New("copy file has child entries")
			}
			parent := path.Dir(name)
			if parent != "." && !seen[parent] {
				return errors.New("copy entry precedes parent")
			}
		}
		target := path.Join(destination, relative)
		if check != nil {
			if err := check(target); err != nil {
				return err
			}
		}
		parent, err := openParent(root, target)
		if err != nil {
			return err
		}
		err = importEntry(ctx, parent, path.Base(target), header, reader)
		parent.Close()
		if err != nil {
			return fmt.Errorf("copy %s: %w", target, err)
		}
		if header.Typeflag == tar.TypeDir {
			directories = append(directories, target)
			headers = append(headers, header)
		}
		beforeEnd := input.n
		header, err = reader.Next()
		if err == io.EOF {
			if input.n-beforeEnd < 1024 || input.n%512 != 0 || input.tail != [1024]byte{} {
				return errors.New("incomplete copy archive terminator")
			}
			var buffer [32768]byte
			for {
				n, err := input.Read(buffer[:])
				for _, b := range buffer[:n] {
					if b != 0 {
						return errors.New("unexpected data after copy archive")
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
	}
	for i := len(directories) - 1; i >= 0; i-- {
		parent, err := openParent(root, directories[i])
		if err != nil {
			return err
		}
		dir, err := parent.OpenRoot(path.Base(directories[i]))
		if err == nil {
			err = dir.Chmod(".", os.FileMode(headers[i].Mode))
			if err == nil {
				err = dir.Chtimes(".", headers[i].ModTime, headers[i].ModTime)
			}
			dir.Close()
		}
		parent.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func openParent(root *os.Root, target string) (*os.Root, error) {
	parent := path.Dir(target)
	current, err := root.OpenRoot(".")
	if err != nil {
		return nil, err
	}
	if parent == "." {
		return current, nil
	}
	for _, component := range strings.Split(parent, "/") {
		info, err := current.Lstat(component)
		if err != nil || !info.IsDir() {
			current.Close()
			return nil, errors.New("copy path parent must be an existing directory without symlinks")
		}
		next, err := current.OpenRoot(component)
		current.Close()
		if err != nil {
			return nil, err
		}
		current = next
	}
	return current, nil
}

func importEntry(ctx context.Context, parent *os.Root, name string, header *tar.Header, reader io.Reader) error {
	info, err := parent.Lstat(name)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if info != nil && info.Mode()&os.ModeSymlink != 0 {
		return errors.New("copy destination is a symlink")
	}
	switch header.Typeflag {
	case tar.TypeDir:
		if info != nil {
			if !info.IsDir() {
				return errors.New("cannot replace a file with a directory")
			}
			return nil
		}
		return parent.Mkdir(name, 0700)
	case tar.TypeReg, tar.TypeSymlink:
		if info != nil && !info.Mode().IsRegular() {
			return errors.New("cannot replace a directory or special file")
		}
	default:
		return errors.New("copy rejects hardlinks and special archive entries")
	}
	file, err := createTemporary(parent)
	if err != nil {
		return err
	}
	temporary := path.Base(file.Name())
	defer func() { file.Close(); parent.Remove(temporary) }()
	if header.Typeflag == tar.TypeSymlink {
		file.Close()
		if err := parent.Remove(temporary); err != nil {
			return err
		}
		if err := parent.Symlink(header.Linkname, temporary); err != nil {
			return err
		}
	} else {
		if _, err := io.CopyN(file, &stream{ctx: ctx, r: reader}, header.Size); err != nil {
			return err
		}
		if err := file.Chmod(os.FileMode(header.Mode)); err != nil {
			return err
		}
		if err := file.Sync(); err != nil {
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
		if err := parent.Chtimes(temporary, header.ModTime, header.ModTime); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return parent.Rename(temporary, name)
}
