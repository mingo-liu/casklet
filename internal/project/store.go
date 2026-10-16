package project

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// State contains resource identities and comparison digests, never run arguments
// or environment values.
// Resource creation timestamps fence manual deletion/recreation of the same name.
type State struct {
	Services  map[string]ServiceReceipt `json:"services,omitempty"`
	Version   int                       `json:"version"`
	Name      string                    `json:"name"`
	Resources map[string]Resource       `json:"resources"`
}

type Resource struct {
	Kind      string    `json:"kind"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

type Store struct {
	root string
	name string
	lock *os.File
}

func DefaultRoot() (string, error) {
	if root := os.Getenv("CASKLET_MACHINE_STATE_DIR"); root != "" {
		if !filepath.IsAbs(root) {
			return "", errors.New("CASKLET_MACHINE_STATE_DIR must be absolute")
		}
		return filepath.Join(root, "projects"), nil
	}
	root, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "casklet", "projects"), nil
}

func safeFile(info os.FileInfo, directory bool) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() || info.Mode().Perm()&0077 != 0 || (directory && !info.IsDir()) || (!directory && (!info.Mode().IsRegular() || stat.Nlink != 1)) {
		return errors.New("unsafe project state ownership, permissions, or type")
	}
	return nil
}

func Open(ctx context.Context, root, name string) (*Store, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := ValidateName(name); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	if err := safeFile(info, true); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(root, "."+name+".lock"), os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	info, err = file.Stat()
	if err == nil {
		err = safeFile(info, false)
	}
	if err != nil {
		file.Close()
		return nil, err
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return &Store{root: root, name: name, lock: file}, nil
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

func (store *Store) Close() error { return store.lock.Close() }
func (store *Store) Load() (State, error) {
	state := State{Version: 1, Name: store.name, Resources: map[string]Resource{}}
	file, err := os.OpenFile(filepath.Join(store.root, store.name+".json"), os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return state, err
	}
	if err := safeFile(info, false); err != nil {
		return state, err
	}
	data, err := io.ReadAll(io.LimitReader(file, 1<<20+1))
	if err != nil {
		return state, err
	}
	if len(data) > 1<<20 {
		return state, errors.New("project state exceeds size limit")
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return state, errors.New("invalid project state")
	}
	if state.Version != 1 || state.Name != store.name || state.Resources == nil || len(state.Resources) > 64 {
		return state, errors.New("invalid project state identity")
	}
	if err := validateServiceReceipts(state.Services); err != nil {
		return state, err
	}
	for key, resource := range state.Resources {
		if key != resource.Kind+":"+resource.Name || (resource.Kind != "network" && resource.Kind != "volume") || resource.CreatedAt.IsZero() || ValidateName(resource.Name) != nil || !strings.HasPrefix(resource.Name, "cp-"+store.name+"-") {
			return state, errors.New("invalid project resource identity")
		}
	}
	return state, nil
}

func (store *Store) Save(state State) error {
	if state.Name != store.name || state.Version != 1 {
		return errors.New("invalid project state identity")
	}
	if err := validateServiceReceipts(state.Services); err != nil {
		return err
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(store.root, ".state-")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	err = errors.Join(err, file.Close())
	if err != nil {
		return err
	}
	if err := os.Rename(name, filepath.Join(store.root, store.name+".json")); err != nil {
		return err
	}
	dir, err := os.Open(store.root)
	if err != nil {
		return err
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("sync project state: %w", err)
	}
	return nil
}
