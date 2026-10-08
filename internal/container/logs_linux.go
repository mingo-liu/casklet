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

	"github.com/mingo-liu/casklet/internal/config"
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

// Metadata lookup is brief; rotation and snapshots use a per-container lock.
// Waiting for that lock never holds the metadata lock or blocks another log.
func (store *Store) lockLog(ctx context.Context, id string, shared bool) (*os.File, Record, error) {
	operation := unix.LOCK_EX
	if shared {
		operation = unix.LOCK_SH
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		lock, err := store.lock(ctx, true)
		if err != nil {
			return nil, Record{}, err
		}
		record, err := store.readRecord(id)
		// Supervisors pinned to an older engine still rotate under the metadata
		// lock. Keep that protocol until they finish or a new supervisor starts.
		if err == nil && shared && !record.LogLocking && !record.Terminal() {
			return lock, record, nil
		}
		var file *os.File
		if err == nil {
			// Create lazily for records written before log locks were introduced.
			file, err = store.openFile(filepath.Join(store.root, id, ".logs"), unix.O_RDWR, true)
		}
		if err == nil {
			err = unix.Flock(int(file.Fd()), operation|unix.LOCK_NB)
		}
		lock.Close()
		if err == nil {
			return file, record, nil
		}
		if file != nil {
			file.Close()
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EINTR) {
			return nil, Record{}, err
		}
		select {
		case <-ctx.Done():
			return nil, Record{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (log *rotatingLog) Write(data []byte) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	lock, _, err := log.store.lockLog(ctx, log.id, false)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			log.truncated = true
			return 0, errors.Join(errLogBusy, err)
		}
		return 0, err
	}
	defer lock.Close()
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
	lock, _, err := log.store.lockLog(ctx, log.id, false)
	if err != nil {
		return err
	}
	defer lock.Close()
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
	file   *os.File
	inode  uint64
	size   int64
	offset int64
}

func (store *Store) logSnapshot(ctx context.Context, id string, cfg config.Config, cursor logCursor, generation uint64) ([]byte, logCursor, error) {
	if cursor.offset < 0 {
		return nil, cursor, errors.New("invalid log cursor offset")
	}
	lock, record, err := store.lockLog(ctx, id, true)
	if err != nil {
		return nil, cursor, err
	}
	defer lock.Close()
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
		offset := int64(0)
		if stat.Ino == cursor.inode {
			start = len(segments)
			if cursor.offset <= stat.Size {
				offset = cursor.offset
			}
		}
		segments = append(segments, logSegment{file: file, inode: stat.Ino, size: stat.Size, offset: offset})
	}
	// Sizes are validated under the log lock. Allocate only the unread bytes
	// once, avoiding per-file growth buffers and repeated concatenation copies.
	if err := ctx.Err(); err != nil {
		return nil, cursor, err
	}
	var length int64
	for _, segment := range segments[start:] {
		length += segment.size - segment.offset
	}
	data := make([]byte, length)
	var position int64
	for _, segment := range segments[start:] {
		if err := ctx.Err(); err != nil {
			return nil, cursor, err
		}
		if _, err := segment.file.Seek(segment.offset, io.SeekStart); err != nil {
			return nil, cursor, err
		}
		end := position + segment.size - segment.offset
		if _, err := io.ReadFull(segment.file, data[position:end]); err != nil {
			return nil, cursor, err
		}
		position = end
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
