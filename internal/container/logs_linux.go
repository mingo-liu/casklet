//go:build linux

package container

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/mingo-liu/mini-docker/internal/config"
	"golang.org/x/sys/unix"
)

type rotatingLog struct {
	store     *Store
	id        string
	cfg       config.Config
	truncated bool
}

func logName(index int) string {
	if index == 0 {
		return "container.log"
	}
	return fmt.Sprintf("container.log.%d", index)
}

// Writes and snapshots share the storage lock. Output to a slow CLI is copied
// after releasing the lock; it cannot hold up capture or container management.
func (log *rotatingLog) Write(data []byte) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	lock, err := log.store.lock(ctx, false)
	if err != nil {
		return 0, err
	}
	defer lock.Close()
	if _, err := log.store.readRecord(log.id); err != nil {
		return 0, err
	}
	size, files := log.cfg.LogRetention()
	dir := filepath.Join(log.store.root, log.id)
	// Validate every artifact before deleting or renaming anything.
	for i := 0; i < files; i++ {
		file, err := log.store.openFile(filepath.Join(dir, logName(i)), unix.O_RDONLY, false)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return 0, err
		}
		file.Close()
	}
	written := 0
	for len(data) > 0 {
		file, err := log.store.openFile(filepath.Join(dir, logName(0)), unix.O_RDWR|unix.O_APPEND, true)
		if err != nil {
			return written, err
		}
		stat, err := file.Stat()
		if err != nil {
			file.Close()
			return written, err
		}
		// Old versions retained a single 16 MiB prefix. Keep its newest segment
		// when first appending with a smaller configured file size.
		if stat.Size() > size {
			tail := make([]byte, size)
			_, err = file.ReadAt(tail, stat.Size()-size)
			if err == nil {
				err = file.Truncate(0)
			}
			if err == nil {
				_, err = file.Write(tail)
			}
			if err != nil {
				file.Close()
				return written, err
			}
			log.truncated = true
		}
		available := size - stat.Size()
		if available <= 0 {
			err = errors.Join(file.Sync(), file.Close())
			if err != nil {
				return written, err
			}
			oldest := filepath.Join(dir, logName(files-1))
			if err := os.Remove(oldest); err == nil {
				log.truncated = true
			} else if !errors.Is(err, os.ErrNotExist) {
				return written, err
			}
			for i := files - 2; i >= 0; i-- {
				if err := os.Rename(filepath.Join(dir, logName(i)), filepath.Join(dir, logName(i+1))); err != nil && !errors.Is(err, os.ErrNotExist) {
					return written, err
				}
			}
			if err := syncDirectory(dir); err != nil {
				return written, err
			}
			continue
		}
		n := len(data)
		if int64(n) > available {
			n = int(available)
		}
		count, writeErr := file.Write(data[:n])
		err = errors.Join(writeErr, file.Close())
		written += count
		if err != nil {
			return written, err
		}
		if count != n {
			return written, io.ErrShortWrite
		}
		data = data[n:]
	}
	return written, nil
}

func (log *rotatingLog) Sync() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	lock, err := log.store.lock(ctx, false)
	if err != nil {
		return err
	}
	defer lock.Close()
	if _, err := log.store.readRecord(log.id); err != nil {
		return err
	}
	// A supervisor can die between rotating the current file and creating its
	// replacement. Recover the missing current file even for a silent command.
	file, err := log.store.openFile(filepath.Join(log.store.root, log.id, logName(0)), unix.O_RDWR, true)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

// A cursor pins an inode rather than a pathname, which changes during rotation.
// If retention overtakes a follower, resume at the oldest still-retained byte.
type logCursor struct {
	inode  uint64
	offset int64
	pin    *os.File
}

type logSegment struct {
	file  *os.File
	inode uint64
	size  int64
}

func (store *Store) logSnapshot(ctx context.Context, id string, cfg config.Config, cursor logCursor, generation uint64) ([]byte, logCursor, error) {
	lock, err := store.lock(ctx, true)
	if err != nil {
		return nil, cursor, err
	}
	defer lock.Close()
	record, err := store.readRecord(id)
	if err != nil {
		return nil, cursor, err
	}
	if record.Generation != generation {
		return nil, cursor, nil
	}
	size, files := cfg.LogRetention()
	segments := make([]logSegment, 0, files)
	defer func() {
		for _, segment := range segments {
			segment.file.Close()
		}
	}()
	start := 0
	var total int64
	for i := files - 1; i >= 0; i-- {
		file, err := store.openFile(filepath.Join(store.root, id, logName(i)), unix.O_RDONLY, false)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, cursor, err
		}
		var stat unix.Stat_t
		if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
			file.Close()
			return nil, cursor, err
		}
		// Permit legacy single-file logs until the next append migrates them.
		limit := size
		if i == 0 && limit < MaxLogBytes {
			limit = MaxLogBytes
		}
		total += stat.Size
		if stat.Size > limit || total > 64<<20 {
			file.Close()
			return nil, cursor, errors.New("container log exceeds the retained size limit")
		}
		if stat.Ino == cursor.inode {
			start = len(segments)
		}
		segments = append(segments, logSegment{file, stat.Ino, stat.Size})
	}
	var data []byte
	for _, segment := range segments[start:] {
		offset := int64(0)
		if segment.inode == cursor.inode && cursor.offset <= segment.size {
			offset = cursor.offset
		}
		if _, err := segment.file.Seek(offset, io.SeekStart); err != nil {
			return nil, cursor, err
		}
		chunk, err := io.ReadAll(io.LimitReader(segment.file, segment.size-offset))
		if err != nil {
			return nil, cursor, err
		}
		data = append(data, chunk...)
		cursor.inode, cursor.offset = segment.inode, segment.size
	}
	if len(segments) > 0 {
		last := segments[len(segments)-1]
		// Keep the current inode alive so fast rotation cannot reuse its identity.
		fd, err := unix.FcntlInt(last.file.Fd(), unix.F_DUPFD_CLOEXEC, 0)
		if err != nil {
			return nil, cursor, err
		}
		if cursor.pin != nil {
			cursor.pin.Close()
		}
		cursor.pin = os.NewFile(uintptr(fd), last.file.Name())
	}
	return data, cursor, nil
}
