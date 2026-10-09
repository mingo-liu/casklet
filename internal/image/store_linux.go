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

const recoveryBatchSize = 32

// Store coordinates publication, readers and deletion across processes.
type Store struct {
	root  string
	owner uint32
	// Tests can pause one owner while another Store accesses the same storage.
	removeTreeFn func(context.Context, string) error
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
	return waitFileLock(ctx, file, shared)
}

func waitFileLock(ctx context.Context, file *os.File, shared bool) (*os.File, error) {
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
	stage, lease, transaction, err := store.beginImport(ctx)
	if err != nil {
		return Record{}, err
	}
	defer lease.Close()
	defer store.cleanupImport(stage, transaction)
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
	record := Record{ID: id, Architecture: runtime.GOARCH, CreatedAt: time.Now().UTC(), SizeBytes: size}
	return store.publish(ctx, stage, record, "")
}

// beginImport pins private staging before releasing the metadata lock. Its
// shared lease moves with publication and also prevents removal until return.
func (store *Store) beginImport(ctx context.Context) (string, *os.File, *os.File, error) {
	if err := store.recover(ctx); err != nil {
		return "", nil, nil, err
	}
	lock, err := store.lock(ctx, false)
	if err != nil {
		return "", nil, nil, err
	}
	defer lock.Close()
	stage, err := os.MkdirTemp(store.root, ".prepare-")
	if err != nil {
		return "", nil, nil, err
	}
	transaction, err := store.openFile(store.transactionPath(stage), unix.O_RDWR|unix.O_CREAT|unix.O_EXCL)
	if err == nil {
		err = unix.Flock(int(transaction.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	}
	if err != nil {
		if transaction != nil {
			transaction.Close()
		}
		os.Remove(stage)
		return "", nil, nil, err
	}
	lease, err := store.openFile(filepath.Join(stage, ".lease"), unix.O_RDWR|unix.O_CREAT|unix.O_EXCL)
	if err == nil {
		err = unix.Flock(int(lease.Fd()), unix.LOCK_SH|unix.LOCK_NB)
	}
	if err != nil {
		if lease != nil {
			lease.Close()
		}
		os.Remove(filepath.Join(stage, ".lease"))
		os.Remove(stage)
		transaction.Close()
		os.Remove(store.transactionPath(stage))
		return "", nil, nil, err
	}
	return stage, lease, transaction, nil
}

func (store *Store) transactionPath(stage string) string {
	return filepath.Join(store.root, ".transaction-"+filepath.Base(stage))
}

// Keep the external transaction lock throughout recursive cleanup: internal
// lease files can disappear before the rest of the tree. Only lock-file removal
// needs the store lock, after no staging tree remains.
func (store *Store) cleanupImport(stage string, transaction *os.File) {
	defer transaction.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := store.removeTree(ctx, stage); err != nil {
		return
	}
	lock, err := store.lock(ctx, false)
	if err != nil {
		return
	}
	defer lock.Close()
	os.Remove(store.transactionPath(stage))
	syncDirectory(store.root)
}

// publish flushes staging without the store lock. Only identity and reference
// publication serialize. A concurrent identical publication is checked under a
// shared image lease, so full verification does not block unrelated metadata.
func (store *Store) publish(ctx context.Context, stage string, record Record, ref string) (Record, error) {
	data, err := json.Marshal(record)
	if err != nil {
		return Record{}, err
	}
	if len(data) > 1<<20 {
		return Record{}, errors.New("image configuration exceeds 1 MiB")
	}
	if err := os.WriteFile(filepath.Join(stage, "image.json"), data, 0600); err != nil {
		return Record{}, err
	}
	if err := rootfs.SyncTree(ctx, stage); err != nil {
		return Record{}, err
	}
	lock, err := store.lock(ctx, false)
	if err != nil {
		return Record{}, err
	}
	defer func() {
		if lock != nil {
			lock.Close()
		}
	}()
	existing, err := store.read(record.ID)
	if err == nil {
		lease, err := store.acquireLeaseLocked(existing.ID)
		if err != nil {
			return Record{}, err
		}
		defer lease.Close()
		lock.Close()
		lock = nil
		if err := store.verify(ctx, existing); err != nil {
			return Record{}, err
		}
		record = existing
		if ref == "" {
			return record, nil
		}
		lock, err = store.lock(ctx, false)
		if err != nil {
			return Record{}, err
		}
	} else if errors.Is(err, ErrNotFound) {
		if err := ctx.Err(); err != nil {
			return Record{}, err
		}
		if err := os.Rename(stage, store.path(record.ID)); err != nil {
			return Record{}, err
		}
		if err := syncDirectory(store.root); err != nil {
			return Record{}, err
		}
	} else {
		return Record{}, err
	}
	if ref != "" {
		if err := store.publishReferenceLocked(ctx, ref, record.ID); err != nil {
			return Record{}, err
		}
	}
	return record, nil
}

type imageDeletion struct {
	path        string
	transaction *os.File
}

// A tombstone has no published identity. Its stable external lock remains held
// even after recursive removal unlinks the tree's internal usage lease.
func (store *Store) newDeletionLocked(ctx context.Context, path string) (*imageDeletion, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Create the lock before a tombstone directory can exist. A missing external
	// lock on an existing tombstone is unsafe, not a reason to invent a new one.
	transaction, err := os.CreateTemp(store.root, ".transaction-.delete-")
	if err != nil {
		return nil, err
	}
	tombstone := filepath.Join(store.root, strings.TrimPrefix(filepath.Base(transaction.Name()), ".transaction-"))
	claimed := false
	defer func() {
		if !claimed {
			transaction.Close()
			os.Remove(transaction.Name())
		}
	}()
	if err := unix.Flock(int(transaction.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return nil, err
	}
	if err := transaction.Sync(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := unix.Renameat2(unix.AT_FDCWD, path, unix.AT_FDCWD, tombstone, unix.RENAME_NOREPLACE); err != nil {
		return nil, err
	}
	claimed = true
	deletion := &imageDeletion{path: tombstone, transaction: transaction}
	return deletion, syncDirectory(store.root)
}

// recover claims abandoned transactions under the metadata lock and removes
// their trees after releasing it. Other operations skip the claimed external
// locks, so a slow cleanup cannot block unrelated image preparation or lookup.
func (store *Store) recover(ctx context.Context) error {
	for {
		lock, err := store.lock(ctx, false)
		if err != nil {
			return err
		}
		deletions, more, claimErr := store.recoverLocked(ctx)
		lock.Close()
		for i, deletion := range deletions {
			if err := store.finishDeletion(ctx, deletion); err != nil {
				for _, pending := range deletions[i+1:] {
					pending.transaction.Close()
				}
				return errors.Join(claimErr, err)
			}
		}
		if claimErr != nil || !more {
			return claimErr
		}
	}
}

func (store *Store) recoverLocked(ctx context.Context) ([]*imageDeletion, bool, error) {
	entries, err := os.ReadDir(store.root)
	if err != nil {
		return nil, false, err
	}
	var deletions []*imageDeletion
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return deletions, false, err
		}
		name := entry.Name()
		path := filepath.Join(store.root, name)
		if strings.HasPrefix(name, ".transaction-.prepare-") || strings.HasPrefix(name, ".transaction-.import-") || strings.HasPrefix(name, ".transaction-.remove-") || strings.HasPrefix(name, ".transaction-.delete-") {
			stage := filepath.Join(store.root, strings.TrimPrefix(name, ".transaction-"))
			if _, err := os.Lstat(stage); err == nil {
				continue
			} else if !errors.Is(err, os.ErrNotExist) {
				return deletions, false, err
			}
			file, err := store.openFile(path, unix.O_RDONLY)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return deletions, false, err
			}
			err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
			if errors.Is(err, unix.EWOULDBLOCK) {
				file.Close()
				continue
			}
			if err == nil {
				err = os.Remove(path)
			}
			file.Close()
			if err != nil {
				return deletions, false, err
			}
			continue
		}
		if strings.HasPrefix(name, ".ref-stage-") {
			file, err := store.openFile(path, unix.O_RDONLY)
			if err != nil {
				return deletions, false, err
			}
			file.Close()
			if err := os.Remove(path); err != nil {
				return deletions, false, err
			}
			continue
		}
		if strings.HasPrefix(name, ".prepare-") || strings.HasPrefix(name, ".import-") || strings.HasPrefix(name, ".remove-") || strings.HasPrefix(name, ".delete-") {
			deletion, err := store.recoverTransactionLocked(ctx, path)
			if deletion != nil {
				deletions = append(deletions, deletion)
			}
			if err != nil {
				return deletions, false, err
			}
			if len(deletions) == recoveryBatchSize {
				return deletions, true, syncDirectory(store.root)
			}
		}
	}
	return deletions, false, syncDirectory(store.root)
}

func (store *Store) recoverTransactionLocked(ctx context.Context, path string) (*imageDeletion, error) {
	isDeletion := strings.HasPrefix(filepath.Base(path), ".delete-")
	transaction, err := store.openFile(store.transactionPath(path), unix.O_RDONLY)
	handoff := false
	if err == nil {
		defer func() {
			if !handoff {
				transaction.Close()
			}
		}()
		err = unix.Flock(int(transaction.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
	} else if isDeletion || !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("unsafe image transaction lock for %s: %w", filepath.Base(path), err)
	}
	if err := store.checkDirectory(path, true); err != nil {
		return nil, err
	}
	lease, err := store.openFile(filepath.Join(path, ".lease"), unix.O_RDONLY)
	if err == nil {
		defer lease.Close()
		err = unix.Flock(int(lease.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := rootfs.CheckUnmounted(path); err != nil {
		return nil, err
	}
	if isDeletion {
		handoff = true
		return &imageDeletion{path: path, transaction: transaction}, nil
	}
	deletion, err := store.newDeletionLocked(ctx, path)
	if deletion == nil {
		return nil, err
	}
	if err == nil && transaction != nil {
		err = os.Remove(store.transactionPath(path))
	}
	return deletion, err
}

func (store *Store) finishDeletion(ctx context.Context, deletion *imageDeletion) error {
	defer deletion.transaction.Close()
	if err := store.removeTree(ctx, deletion.path); err != nil {
		return err
	}
	// Keep the transaction lock while persisting directory removal. Its orphan
	// file can be removed safely by a later recovery if this owner is canceled.
	if err := syncDirectory(store.root); err != nil {
		return err
	}
	lock, err := store.lock(ctx, false)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := os.Remove(store.transactionPath(deletion.path)); err != nil {
		return err
	}
	return syncDirectory(store.root)
}

func (store *Store) removeTree(ctx context.Context, path string) error {
	if store.removeTreeFn != nil {
		return store.removeTreeFn(ctx, path)
	}
	return removeImageTree(ctx, path)
}

// Remove leaf entries through pinned directories without following symlinks or
// crossing mounts. Read entries in bounded batches and check cancellation before
// each mutation, including the final removal of the tombstone itself.
func removeImageTree(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("image cleanup requires a real directory")
	}
	if err := rootfs.CheckUnmounted(path); err != nil {
		return err
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	root := os.NewFile(uintptr(fd), "image-cleanup-root")
	defer root.Close()
	if err := removeImageEntries(ctx, root); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Remove(path)
}

func removeImageEntries(ctx context.Context, directory *os.File) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, readErr := directory.ReadDir(128)
		if readErr != nil && readErr != io.EOF {
			return readErr
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			flags := 0
			if entry.IsDir() {
				fd, err := unix.Openat2(int(directory.Fd()), entry.Name(), &unix.OpenHow{
					Flags:   unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC,
					Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_XDEV,
				})
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				if err != nil {
					return fmt.Errorf("open image cleanup directory %s without links or mounts: %w", entry.Name(), err)
				}
				child := os.NewFile(uintptr(fd), "image-cleanup-directory")
				err = removeImageEntries(ctx, child)
				closeErr := child.Close()
				if err != nil || closeErr != nil {
					return errors.Join(err, closeErr)
				}
				flags = unix.AT_REMOVEDIR
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := unix.Unlinkat(int(directory.Fd()), entry.Name(), flags); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		if readErr == io.EOF {
			return nil
		}
	}
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
	record, lease, err := func() (Record, *os.File, error) {
		lock, err := store.lock(ctx, true)
		if err != nil {
			return Record{}, nil, err
		}
		defer lock.Close()
		record, err := store.resolveIDLocked(ctx, id)
		if err != nil {
			return Record{}, nil, err
		}
		if record.Architecture != runtime.GOARCH {
			return Record{}, nil, errors.New("image architecture does not match the runtime")
		}
		lease, err := store.acquireLeaseLocked(record.ID)
		return record, lease, err
	}()
	if err != nil {
		return Record{}, "", nil, err
	}
	// The image lease pins metadata and content while verification walks the
	// tree without the global store lock.
	if err := store.verify(ctx, record); err != nil {
		lease.Close()
		return Record{}, "", nil, err
	}
	return record, filepath.Join(store.path(record.ID), "rootfs"), lease, nil
}

func (store *Store) acquireLeaseLocked(id string) (*os.File, error) {
	lease, err := store.openFile(filepath.Join(store.path(id), ".lease"), unix.O_RDONLY)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(lease.Fd()), unix.LOCK_SH|unix.LOCK_NB); err != nil {
		lease.Close()
		return nil, err
	}
	return lease, nil
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
	if err := store.recover(ctx); err != nil {
		return err
	}
	lock, err := store.lock(ctx, false)
	if err != nil {
		return err
	}
	defer func() {
		if lock != nil {
			lock.Close()
		}
	}()
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
	if err := rootfs.CheckUnmounted(store.path(id)); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if dryRun {
		return nil
	}
	deletion, claimErr := store.newDeletionLocked(ctx, store.path(id))
	lock.Close()
	lock = nil
	if deletion == nil {
		return claimErr
	}
	return errors.Join(claimErr, store.finishDeletion(ctx, deletion))
}
