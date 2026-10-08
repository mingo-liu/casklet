//go:build linux

package volume

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mingo-liu/casklet/internal/config"
	"github.com/mingo-liu/casklet/internal/rootfs"
	"golang.org/x/sys/unix"
)

type Store struct {
	root  string
	owner uint32
}

func OpenStore() (*Store, error) {
	if os.Geteuid() != 0 {
		return nil, errors.New("named volumes require root privileges inside Linux")
	}
	for _, p := range []string{"/var/lib/casklet", config.VolumeRoot} {
		if err := os.Mkdir(p, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		private := p == config.VolumeRoot
		if err := checkDirectory(p, 0, private); err != nil {
			return nil, err
		}
		var st unix.Stat_t
		if err := unix.Lstat(p, &st); err != nil {
			return nil, err
		}
		if st.Uid != 0 || st.Mode&0022 != 0 {
			return nil, errors.New("unsafe volume storage parent")
		}
	}
	return &Store{root: config.VolumeRoot}, nil
}
func checkDirectory(path string, owner uint32, private bool) error {
	var st unix.Stat_t
	if err := unix.Lstat(path, &st); err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR || private && (st.Uid != owner || st.Mode&0077 != 0) {
		return fmt.Errorf("unsafe volume directory: %s", path)
	}
	return nil
}
func (s *Store) openLock(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		f.Close()
		return nil, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != s.owner || st.Mode&0077 != 0 || st.Nlink != 1 {
		f.Close()
		return nil, errors.New("unsafe volume lock")
	}
	return f, nil
}
func (s *Store) lock(ctx context.Context) (*os.File, error) {
	if err := checkDirectory(s.root, s.owner, true); err != nil {
		return nil, err
	}
	f, err := s.openLock(filepath.Join(s.root, ".lock"))
	if err != nil {
		return nil, err
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			f.Close()
			return nil, err
		}
		err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EINTR) {
			f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}
func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
func (s *Store) read(name string) (Record, error) {
	if err := config.ValidateVolumeName(name); err != nil {
		return Record{}, err
	}
	p := filepath.Join(s.root, name)
	if err := checkDirectory(p, s.owner, true); err != nil {
		return Record{}, err
	}
	if err := checkDirectory(filepath.Join(p, "data"), s.owner, false); err != nil {
		return Record{}, err
	}
	fd, err := unix.Open(filepath.Join(p, "volume.json"), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return Record{}, err
	}
	f := os.NewFile(uintptr(fd), "volume.json")
	defer f.Close()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return Record{}, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != s.owner || st.Mode&0077 != 0 || st.Nlink != 1 || st.Size > 4096 {
		return Record{}, errors.New("unsafe volume metadata")
	}
	data, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil {
		return Record{}, err
	}
	var r Record
	if len(data) > 4096 || json.Unmarshal(data, &r) != nil || r.Name != name || r.CreatedAt.IsZero() {
		return r, errors.New("invalid volume metadata")
	}
	return r, nil
}
func (s *Store) recover(ctx context.Context) error {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !strings.HasPrefix(e.Name(), ".create-") && !strings.HasPrefix(e.Name(), ".remove-") {
			continue
		}
		p := filepath.Join(s.root, e.Name())
		if err := checkDirectory(p, s.owner, true); err != nil {
			return err
		}
		// A restore releases the global lock while extracting, retaining its lease.
		leasePath := filepath.Join(p, ".lease")
		if _, err := os.Lstat(leasePath); err == nil {
			lease, err := s.openLock(leasePath)
			if err != nil {
				return err
			}
			err = unix.Flock(int(lease.Fd()), unix.LOCK_EX|unix.LOCK_NB)
			if errors.Is(err, unix.EWOULDBLOCK) {
				lease.Close()
				continue
			}
			if err != nil {
				lease.Close()
				return err
			}
			defer lease.Close()
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := rootfs.CheckUnmounted(p); err != nil {
			return err
		}
		if err := os.RemoveAll(p); err != nil {
			return err
		}
	}
	return syncDir(s.root)
}
func (s *Store) Create(ctx context.Context, name string) (Record, error) {
	if err := config.ValidateVolumeName(name); err != nil {
		return Record{}, err
	}
	lock, err := s.lock(ctx)
	if err != nil {
		return Record{}, err
	}
	defer lock.Close()
	if err := s.recover(ctx); err != nil {
		return Record{}, err
	}
	if r, err := s.read(name); err == nil {
		return r, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return Record{}, err
	}
	// An incomplete existing directory must never be replaced.
	if _, err := os.Lstat(filepath.Join(s.root, name)); !errors.Is(err, os.ErrNotExist) {
		return Record{}, errors.New("volume already exists with invalid contents")
	}
	stage, err := os.MkdirTemp(s.root, ".create-")
	if err != nil {
		return Record{}, err
	}
	defer os.RemoveAll(stage)
	if err := os.Mkdir(filepath.Join(stage, "data"), 0755); err != nil {
		return Record{}, err
	}
	f, err := s.openLock(filepath.Join(stage, ".lease"))
	if err != nil {
		return Record{}, err
	}
	f.Close()
	r := Record{Name: name, CreatedAt: time.Now().UTC()}
	data, _ := json.Marshal(r)
	if err := os.WriteFile(filepath.Join(stage, "volume.json"), data, 0600); err != nil {
		return Record{}, err
	}
	mf, err := os.Open(filepath.Join(stage, "volume.json"))
	if err != nil {
		return Record{}, err
	}
	err = mf.Sync()
	mf.Close()
	if err != nil {
		return Record{}, err
	}
	if err := syncDir(stage); err != nil {
		return Record{}, err
	}
	if err := os.Rename(stage, filepath.Join(s.root, name)); err != nil {
		return Record{}, err
	}
	return r, syncDir(s.root)
}
func (s *Store) List(ctx context.Context) ([]Record, error) {
	lock, err := s.lock(ctx)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	if err := s.recover(ctx); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, err
	}
	records := []Record{}
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		r, err := s.read(e.Name())
		if err != nil {
			return nil, err
		}
		records = append(records, r)
	}
	return records, nil
}
func (s *Store) Inspect(ctx context.Context, name string) (Record, error) {
	lock, err := s.lock(ctx)
	if err != nil {
		return Record{}, err
	}
	defer lock.Close()
	return s.read(name)
}
func (s *Store) Acquire(ctx context.Context, name string) (*os.File, error) {
	lock, err := s.lock(ctx)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	if _, err := s.read(name); err != nil {
		return nil, err
	}
	f, err := s.openLock(filepath.Join(s.root, name, ".lease"))
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_SH|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, ErrInUse
	}
	return f, nil
}
func (s *Store) Remove(ctx context.Context, name string, referenced ReferenceCheck) error {
	lock, err := s.lock(ctx)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := s.recover(ctx); err != nil {
		return err
	}
	if _, err := s.read(name); err != nil {
		return err
	}
	p := filepath.Join(s.root, name)
	f, err := s.openLock(filepath.Join(p, ".lease"))
	if err != nil {
		return err
	}
	defer f.Close()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return ErrInUse
	}
	if referenced == nil {
		return errors.New("volume removal requires a reference check")
	}
	used, err := referenced(ctx, name)
	if err != nil {
		return err
	}
	if used {
		return ErrInUse
	}
	if err := rootfs.CheckUnmounted(p); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	tomb := filepath.Join(s.root, ".remove-"+name)
	if err := os.Rename(p, tomb); err != nil {
		return err
	}
	if err := syncDir(s.root); err != nil {
		return err
	}
	if err := os.RemoveAll(tomb); err != nil {
		return err
	}
	return syncDir(s.root)
}
