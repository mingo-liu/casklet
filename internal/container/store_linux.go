//go:build linux

package container

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mingo-liu/mini-docker/internal/config"
	"github.com/mingo-liu/mini-docker/internal/rootfs"
	"golang.org/x/sys/unix"
)

const (
	storeRoot            = "/var/lib/mini-docker/containers"
	maxRecordBytes int64 = 64 * 1024
	maxConfigBytes int64 = 1024 * 1024
)

// Store coordinates metadata updates across independent CLI and supervisor processes.
type Store struct {
	root  string
	owner uint32
}

// OpenStore opens root-owned persistent storage. Host runtime files are never trusted blindly.
func OpenStore() (*Store, error) {
	if os.Geteuid() != 0 {
		return nil, errors.New("container management requires root privileges")
	}
	store := &Store{root: storeRoot, owner: 0}
	for _, dir := range []string{"/", "/var", "/var/lib", "/var/lib/mini-docker", storeRoot} {
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
	if err := store.checkDirectory(root, true); err != nil {
		return nil, err
	}
	return store, nil
}

func (store *Store) checkDirectory(path string, private bool) error {
	var stat unix.Stat_t
	if err := unix.Lstat(path, &stat); err != nil {
		return err
	}
	badPerm := uint32(0022)
	if private {
		badPerm = 0077
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Uid != store.owner || stat.Mode&badPerm != 0 {
		return fmt.Errorf("unsafe container directory: %s", path)
	}
	return nil
}

func (store *Store) openFile(path string, flags int, create bool) (*os.File, error) {
	flags |= unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
	if create {
		flags |= unix.O_CREAT
	}
	fd, err := unix.Open(path, flags, 0600)
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
		return nil, fmt.Errorf("container file must be private, singly linked, and owned by the storage owner: %s", path)
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
	file, err := store.openFile(filepath.Join(store.root, ".lock"), unix.O_RDWR, true)
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

// Create publishes the full configuration, log, lease, and initial record atomically.
func (store *Store) Create(ctx context.Context, cfg config.Config, name string) (Record, error) {
	if err := cfg.ValidateExecution(); err != nil {
		return Record{}, err
	}
	if name != "" {
		if err := ValidateName(name); err != nil {
			return Record{}, err
		}
	}
	bootID, err := currentBootID()
	if err != nil {
		return Record{}, err
	}
	lock, err := store.lock(ctx, false)
	if err != nil {
		return Record{}, err
	}
	defer lock.Close()
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return Record{}, err
	}
	id := hex.EncodeToString(random[:])
	if name == "" {
		name = "mini-" + id[:12]
	}
	records, err := store.listLocked()
	if err != nil {
		return Record{}, err
	}
	for _, record := range records {
		if record.Name == name {
			return Record{}, fmt.Errorf("%w: %s", ErrNameInUse, name)
		}
	}
	path := filepath.Join(store.root, ".create-"+id)
	if err := os.Mkdir(path, 0700); err != nil {
		return Record{}, err
	}
	published := false
	defer func() {
		if !published {
			os.RemoveAll(path)
		}
	}()
	record := Record{RetainRootFS: true, Version: 1, ID: id, BootID: bootID, Name: name, State: StateCreated, CreatedAt: time.Now().UTC(), Command: append([]string(nil), cfg.Command...)}
	if err := store.writeJSON(path, "config.json", cfg, maxConfigBytes); err != nil {
		return Record{}, err
	}
	for _, filename := range []string{".lease", ".operation", "container.log"} {
		file, err := store.openFile(filepath.Join(path, filename), unix.O_RDWR|unix.O_EXCL, true)
		if err != nil {
			return Record{}, err
		}
		if err := file.Close(); err != nil {
			return Record{}, err
		}
	}
	if err := store.writeJSON(path, "state.json", record, maxRecordBytes); err != nil {
		return Record{}, err
	}
	final := filepath.Join(store.root, id)
	if _, err := os.Lstat(final); err == nil {
		return Record{}, os.ErrExist
	} else if !errors.Is(err, os.ErrNotExist) {
		return Record{}, err
	}
	if err := os.Rename(path, final); err != nil {
		return Record{}, err
	}
	published = true
	if err := syncDirectory(store.root); err != nil {
		return Record{}, err
	}
	return record, nil
}

func (store *Store) readJSON(path string, limit int64, target any) error {
	file, err := store.openFile(path, unix.O_RDONLY, false)
	if err != nil {
		return err
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		return err
	}
	if stat.Size() > limit {
		return errors.New("container metadata exceeds its size limit")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return err
	}
	if int64(len(data)) > limit {
		return errors.New("container metadata exceeds its size limit")
	}
	return json.Unmarshal(data, target)
}

func (store *Store) writeJSON(dir, name string, value any, limit int64) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if int64(len(data)) > limit {
		return errors.New("container metadata exceeds its size limit")
	}
	path := filepath.Join(dir, name)
	// Refuse to replace an unexpected artifact, including an existing hard link.
	if _, err := os.Lstat(path); err == nil {
		file, err := store.openFile(path, unix.O_RDONLY, false)
		if err != nil {
			return err
		}
		file.Close()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	file, err := os.CreateTemp(dir, ".json-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	return syncDirectory(dir)
}

func syncDirectory(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

func (store *Store) readRecord(id string) (Record, error) {
	if err := validateID(id); err != nil {
		return Record{}, err
	}
	path := filepath.Join(store.root, id)
	if err := store.checkDirectory(path, true); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Record{}, fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		return Record{}, err
	}
	var record Record
	if err := store.readJSON(filepath.Join(path, "state.json"), maxRecordBytes, &record); err != nil {
		return Record{}, fmt.Errorf("read container %s: %w", id, err)
	}
	return record, validateRecord(record, id)
}

func (store *Store) listLocked() ([]Record, error) {
	entries, err := os.ReadDir(store.root)
	if err != nil {
		return nil, err
	}
	records := make([]Record, 0, len(entries))
	for _, entry := range entries {
		if !containerID.MatchString(entry.Name()) {
			continue
		}
		record, err := store.readRecord(entry.Name())
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].CreatedAt.Equal(records[j].CreatedAt) {
			return records[i].ID < records[j].ID
		}
		return records[i].CreatedAt.Before(records[j].CreatedAt)
	})
	return records, nil
}

func (store *Store) resolveLocked(ref string) (Record, error) {
	if err := validateReference(ref); err != nil {
		return Record{}, err
	}
	if containerID.MatchString(ref) {
		return store.readRecord(ref)
	}
	records, err := store.listLocked()
	if err != nil {
		return Record{}, err
	}
	for _, record := range records {
		if record.Name == ref {
			return record, nil
		}
	}
	return Record{}, fmt.Errorf("%w: %s", ErrNotFound, ref)
}

func (store *Store) Get(ctx context.Context, ref string) (Record, error) {
	lock, err := store.lock(ctx, true)
	if err != nil {
		return Record{}, err
	}
	defer lock.Close()
	return store.resolveLocked(ref)
}

func (store *Store) List(ctx context.Context) ([]Record, error) {
	lock, err := store.lock(ctx, true)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	return store.listLocked()
}

func (store *Store) Update(ctx context.Context, id string, update func(*Record) error) error {
	lock, err := store.lock(ctx, false)
	if err != nil {
		return err
	}
	defer lock.Close()
	record, err := store.readRecord(id)
	if err != nil {
		return err
	}
	originalName := record.Name
	if err := update(&record); err != nil {
		return err
	}
	if record.Name != originalName {
		return errors.New("container name is immutable")
	}
	if err := validateRecord(record, id); err != nil {
		return err
	}
	return store.writeJSON(filepath.Join(store.root, id), "state.json", record, maxRecordBytes)
}

func (store *Store) Config(ctx context.Context, id string) (config.Config, error) {
	lock, err := store.lock(ctx, true)
	if err != nil {
		return config.Config{}, err
	}
	defer lock.Close()
	if _, err := store.readRecord(id); err != nil {
		return config.Config{}, err
	}
	var cfg config.Config
	if err := store.readJSON(filepath.Join(store.root, id, "config.json"), maxConfigBytes, &cfg); err != nil {
		return config.Config{}, err
	}
	return cfg, cfg.ValidateExecution()
}

// AcquireLease is nonblocking. Keep the returned file open for the supervisor's lifetime.
func (store *Store) AcquireLease(ctx context.Context, id string) (*os.File, error) {
	lock, err := store.lock(ctx, true)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	if _, err := store.readRecord(id); err != nil {
		return nil, err
	}
	file, err := store.openFile(filepath.Join(store.root, id, ".lease"), unix.O_RDWR, false)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrBusy
		}
		return nil, err
	}
	return file, nil
}

func (store *Store) OpenLog(ctx context.Context, id string, write bool) (*os.File, error) {
	lock, err := store.lock(ctx, true)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	if _, err := store.readRecord(id); err != nil {
		return nil, err
	}
	flags := unix.O_RDONLY
	if write {
		flags = unix.O_WRONLY | unix.O_APPEND
	}
	return store.openFile(filepath.Join(store.root, id, "container.log"), flags, false)
}

// Remove requires both a terminal record and an unclaimed supervisor lease.
func (store *Store) Remove(ctx context.Context, id string) error {
	lock, err := store.lock(ctx, false)
	if err != nil {
		return err
	}
	defer lock.Close()
	record, err := store.readRecord(id)
	if err != nil {
		return err
	}
	if !record.Terminal() {
		return ErrNotTerminal
	}
	lease, err := store.openFile(filepath.Join(store.root, id, ".lease"), unix.O_RDWR, false)
	if err != nil {
		return err
	}
	defer lease.Close()
	if err := unix.Flock(int(lease.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) {
			return ErrBusy
		}
		return err
	}
	if err := rootfs.CheckUnmounted(filepath.Join(store.root, id)); err != nil {
		return err
	}
	// Hide the record before deleting its contents so a crash cannot leave a
	// half-removed container in the next listing. The held global lock makes
	// name reuse and lookup atomic with this publication change.
	tombstone := filepath.Join(store.root, ".remove-"+id)
	if _, err := os.Lstat(tombstone); err == nil {
		return errors.New("container removal artifact already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(filepath.Join(store.root, id), tombstone); err != nil {
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

// currentBootID distinguishes persistent records from an earlier system boot.
func currentBootID() (string, error) {
	file, err := os.Open("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", fmt.Errorf("read system boot ID: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 129))
	if err != nil {
		return "", fmt.Errorf("read system boot ID: %w", err)
	}
	if len(data) > 128 {
		return "", errors.New("system boot ID exceeds its size limit")
	}
	id := strings.TrimSpace(string(data))
	if !bootIDPattern.MatchString(id) {
		return "", errors.New("system boot ID is not a lowercase UUID")
	}
	return id, nil
}
