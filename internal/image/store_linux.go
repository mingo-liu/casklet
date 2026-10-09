//go:build linux

package image

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/mingo-liu/casklet/internal/rootfs"
	"golang.org/x/sys/unix"
)

const storeRoot = "/var/lib/casklet/images"

// Store coordinates publication, readers and deletion across processes.
type Store struct {
	root  string
	owner uint32
}

func OpenStore() (*Store, error) {
	if os.Geteuid() != 0 {
		return nil, errors.New("local image management requires root privileges")
	}
	store := &Store{root: storeRoot, owner: 0}
	for _, dir := range []string{"/", "/var", "/var/lib", "/var/lib/casklet", storeRoot} {
		if err := os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if err := store.checkDirectory(dir, dir == storeRoot); err != nil {
			return nil, err
		}
	}
	return store, nil
}

func newStoreAt(root string) (*Store, error) {
	store := &Store{root: root, owner: uint32(os.Geteuid())}
	if err := os.Mkdir(root, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	return store, store.checkDirectory(root, true)
}

func (store *Store) checkDirectory(path string, private bool) error {
	var stat unix.Stat_t
	if err := unix.Lstat(path, &stat); err != nil {
		return err
	}
	forbidden := uint32(0022)
	if private {
		forbidden = 0077
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Uid != store.owner || stat.Mode&forbidden != 0 {
		return fmt.Errorf("unsafe image directory: %s", path)
	}
	return nil
}

func (store *Store) openFile(path string, flags int) (*os.File, error) {
	fd, err := unix.Open(path, flags|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		file.Close()
		return nil, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Uid != store.owner || stat.Mode&0077 != 0 || stat.Nlink != 1 {
		file.Close()
		return nil, fmt.Errorf("unsafe image file: %s", path)
	}
	return file, nil
}

func (store *Store) lock(ctx context.Context, shared bool) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := store.checkDirectory(store.root, true); err != nil {
		return nil, err
	}
	file, err := store.openFile(filepath.Join(store.root, ".lock"), unix.O_RDWR|unix.O_CREAT)
	if err != nil {
		return nil, err
	}
	operation := unix.LOCK_EX
	if shared {
		operation = unix.LOCK_SH
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			file.Close()
			return nil, err
		}
		err := unix.Flock(int(file.Fd()), operation|unix.LOCK_NB)
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

func syncDirectory(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

func (store *Store) path(id string) string {
	return filepath.Join(store.root, strings.TrimPrefix(id, "sha256:"))
}

func (store *Store) read(id string) (Record, error) {
	if err := ValidateID(id); err != nil {
		return Record{}, err
	}
	path := store.path(id)
	if err := store.checkDirectory(path, true); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Record{}, fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		return Record{}, err
	}
	file, err := store.openFile(filepath.Join(path, "image.json"), unix.O_RDONLY)
	if err != nil {
		return Record{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil {
		return Record{}, err
	}
	if len(data) > 1<<20 {
		return Record{}, errors.New("image metadata exceeds its size limit")
	}
	var record Record
	if err := json.Unmarshal(data, &record); err != nil {
		return Record{}, err
	}
	if record.ID != id || record.CreatedAt.IsZero() || record.SizeBytes < 0 || (record.Architecture != "arm64" && record.Architecture != "amd64") {
		return Record{}, errors.New("invalid image metadata identity")
	}
	if record.Config != nil {
		if err := ValidateID(record.ManifestDigest); err != nil {
			return Record{}, err
		}
		if err := validateLaunch(record.Config); err != nil {
			return Record{}, err
		}
	}
	return record, nil
}

func overlaps(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

// Import requires a stable directory tree, pins it without symlinks and copies
// through os.Root so replaced descendants cannot read outside the source tree.
func (store *Store) Import(ctx context.Context, source string) (Record, error) {
	if source == "" {
		return Record{}, errors.New("image import requires a rootfs directory")
	}
	absolute, err := filepath.Abs(source)
	if err != nil {
		return Record{}, err
	}
	for _, forbidden := range []string{"/proc", "/dev", "/sys", "/var/lib/casklet", store.root} {
		if absolute == "/" || overlaps(absolute, forbidden) {
			return Record{}, errors.New("image source overlaps protected host storage")
		}
	}
	fd, err := unix.Openat2(unix.AT_FDCWD, absolute, &unix.OpenHow{Flags: unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return Record{}, fmt.Errorf("open image source without symlinks: %w", err)
	}
	defer unix.Close(fd)
	sourceRoot, err := os.OpenRoot(fmt.Sprintf("/proc/self/fd/%d", fd))
	if err != nil {
		return Record{}, err
	}
	defer sourceRoot.Close()
	lock, err := store.lock(ctx, false)
	if err != nil {
		return Record{}, err
	}
	defer lock.Close()
	if err := store.recoverLocked(); err != nil {
		return Record{}, err
	}
	stage, err := os.MkdirTemp(store.root, ".import-")
	if err != nil {
		return Record{}, err
	}
	defer os.RemoveAll(stage)
	tree := filepath.Join(stage, "rootfs")
	if err := os.Mkdir(tree, 0700); err != nil {
		return Record{}, err
	}
	if err := rootfs.CopyFromRoot(ctx, sourceRoot, absolute, tree); err != nil {
		return Record{}, fmt.Errorf("import rootfs: %w", err)
	}
	if _, err := rootfs.Validate(tree); err != nil {
		return Record{}, err
	}
	id, size, err := Identity(ctx, tree, runtime.GOARCH)
	if err != nil {
		return Record{}, err
	}
	if record, err := store.read(id); err == nil {
		if err := store.verify(ctx, record); err != nil {
			return Record{}, err
		}
		return record, nil
	} else if !errors.Is(err, ErrNotFound) {
		return Record{}, err
	}
	record := Record{ID: id, Architecture: runtime.GOARCH, CreatedAt: time.Now().UTC(), SizeBytes: size}
	data, err := json.Marshal(record)
	if err != nil {
		return Record{}, err
	}
	if err := os.WriteFile(filepath.Join(stage, "image.json"), data, 0600); err != nil {
		return Record{}, err
	}
	file, err := store.openFile(filepath.Join(stage, ".lease"), unix.O_RDWR|unix.O_CREAT|unix.O_EXCL)
	if err != nil {
		return Record{}, err
	}
	file.Close()
	// Flush the full tree and metadata before atomically publishing the identity.
	if err := rootfs.SyncTree(ctx, stage); err != nil {
		return Record{}, err
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	if err := os.Rename(stage, store.path(id)); err != nil {
		return Record{}, err
	}
	if err := syncDirectory(store.root); err != nil {
		return Record{}, err
	}
	return record, nil
}

// Staging entries are private and never referenced. The exclusive lock ensures
// no live import or remove owns them after an interrupted operation.
func (store *Store) recoverLocked() error {
	entries, err := os.ReadDir(store.root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".ref-stage-") {
			file, err := store.openFile(filepath.Join(store.root, entry.Name()), unix.O_RDONLY)
			if err != nil {
				return err
			}
			file.Close()
			if err := os.Remove(filepath.Join(store.root, entry.Name())); err != nil {
				return err
			}
			continue
		}
		if !strings.HasPrefix(entry.Name(), ".import-") && !strings.HasPrefix(entry.Name(), ".remove-") {
			continue
		}
		path := filepath.Join(store.root, entry.Name())
		if err := store.checkDirectory(path, true); err != nil {
			return err
		}
		if err := rootfs.CheckUnmounted(path); err != nil {
			return err
		}
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	}
	return nil
}

func (store *Store) List(ctx context.Context) ([]Record, error) {
	lock, err := store.lock(ctx, true)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	entries, err := os.ReadDir(store.root)
	if err != nil {
		return nil, err
	}
	records := make([]Record, 0)
	references := map[string][]string{}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".ref-") || strings.HasPrefix(entry.Name(), ".ref-stage-") {
			continue
		}
		cached, err := store.readReference(entry.Name())
		if err != nil {
			return nil, err
		}
		references[cached.ID] = append(references[cached.ID], cached.Reference)
	}
	for _, entry := range entries {
		id := "sha256:" + entry.Name()
		if ValidateID(id) != nil {
			continue
		}
		record, err := store.read(id)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
		records[len(records)-1].References = references[record.ID]
	}
	sort.Slice(records, func(i, j int) bool { return records[i].ID < records[j].ID })
	return records, nil
}

// resolveIDLocked pins a unique local ID while the caller holds the store lock.
func (store *Store) resolveIDLocked(ctx context.Context, reference string) (Record, error) {
	if err := ValidateIDReference(reference); err != nil {
		return Record{}, err
	}
	prefix := strings.TrimPrefix(reference, "sha256:")
	if len(prefix) == 64 {
		return store.read("sha256:" + prefix)
	}
	entries, err := os.ReadDir(store.root)
	if err != nil {
		return Record{}, err
	}
	id := ""
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return Record{}, err
		}
		candidate := "sha256:" + entry.Name()
		if ValidateID(candidate) == nil && strings.HasPrefix(entry.Name(), prefix) {
			if id != "" {
				return Record{}, fmt.Errorf("%w: %s; use a longer ID", ErrAmbiguousID, reference)
			}
			id = candidate
		}
	}
	if id == "" {
		return Record{}, fmt.Errorf("%w: %s", ErrNotFound, reference)
	}
	return store.read(id)
}

// Acquire prevents deletion until its shared lease is closed. The global lock
// covers lookup and lease acquisition so removal cannot race between them.
func (store *Store) Acquire(ctx context.Context, id string) (Record, string, *os.File, error) {
	lock, err := store.lock(ctx, true)
	if err != nil {
		return Record{}, "", nil, err
	}
	defer lock.Close()
	record, err := store.resolveIDLocked(ctx, id)
	if err != nil {
		return Record{}, "", nil, err
	}
	if record.Architecture != runtime.GOARCH {
		return Record{}, "", nil, errors.New("image architecture does not match the runtime")
	}
	id = record.ID
	lease, err := store.openFile(filepath.Join(store.path(id), ".lease"), unix.O_RDONLY)
	if err != nil {
		return Record{}, "", nil, err
	}
	if err := unix.Flock(int(lease.Fd()), unix.LOCK_SH|unix.LOCK_NB); err != nil {
		lease.Close()
		return Record{}, "", nil, err
	}
	tree := filepath.Join(store.path(id), "rootfs")
	if err := store.verify(ctx, record); err != nil {
		lease.Close()
		return Record{}, "", nil, err
	}
	return record, tree, lease, nil
}

func (store *Store) verify(ctx context.Context, record Record) error {
	tree := filepath.Join(store.path(record.ID), "rootfs")
	info, err := os.Lstat(tree)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("image rootfs must be a real directory")
	}
	if err := rootfs.CheckUnmounted(store.path(record.ID)); err != nil {
		return err
	}
	digest, size, err := Identity(ctx, tree, record.Architecture, record.Config)
	if err != nil {
		return fmt.Errorf("verify image content: %w", err)
	}
	if digest != record.ID || size != record.SizeBytes {
		return errors.New("image content does not match its identity")
	}
	_, err = rootfs.Validate(tree)
	return err
}

func (store *Store) Remove(ctx context.Context, id string, referenced ReferenceCheck) error {
	return store.remove(ctx, id, referenced, false)
}

func (store *Store) remove(ctx context.Context, id string, referenced ReferenceCheck, dryRun bool) error {
	lock, err := store.lock(ctx, false)
	if err != nil {
		return err
	}
	defer lock.Close()
	record, err := store.resolveIDLocked(ctx, id)
	if err != nil {
		return err
	}
	id = record.ID
	lease, err := store.openFile(filepath.Join(store.path(id), ".lease"), unix.O_RDONLY)
	if err != nil {
		return err
	}
	defer lease.Close()
	if err := unix.Flock(int(lease.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) {
			return ErrInUse
		}
		return err
	}
	if referenced == nil {
		return errors.New("image deletion requires a container reference check")
	}
	inUse, err := referenced(ctx, id)
	if err != nil {
		return err
	}
	if inUse {
		return ErrInUse
	}
	if err := store.recoverLocked(); err != nil {
		return err
	}
	if err := rootfs.CheckUnmounted(store.path(id)); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if dryRun {
		return nil
	}
	tombstone := filepath.Join(store.root, ".remove-"+strings.TrimPrefix(id, "sha256:"))
	if err := os.Rename(store.path(id), tombstone); err != nil {
		return err
	}
	if err := syncDirectory(store.root); err != nil {
		return err
	}
	if err := os.RemoveAll(tombstone); err != nil {
		return err
	}
	return syncDirectory(store.root)
}
