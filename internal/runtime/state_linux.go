//go:build linux

package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

const runsRoot = "/var/lib/mini-docker/runs"

type runDirectory struct {
	path string
	lock *os.File
	keep bool
}

type runMetadata struct {
	Cgroup string `json:"cgroup"`
}

func secureDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*unix.Stat_t)
	// os.FileInfo uses syscall.Stat_t, whose type is not unix.Stat_t.
	var actual unix.Stat_t
	if !ok {
		if err := unix.Lstat(path, &actual); err != nil {
			return err
		}
		stat = &actual
	}
	if !info.IsDir() || info.Mode().Perm()&0022 != 0 || stat.Uid != 0 {
		return fmt.Errorf("runtime directory must be a real root-owned directory without group or other write permissions: %s", path)
	}
	return nil
}

func ensureRunsRoot() error {
	for _, dir := range []string{"/var", "/var/lib", "/var/lib/mini-docker", runsRoot} {
		if err := os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		if err := secureDirectory(dir); err != nil {
			return err
		}
	}
	return nil
}

func openRunLock(path string, create bool) (*os.File, error) {
	flags := unix.O_RDWR | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
	if create {
		flags |= unix.O_CREAT | unix.O_EXCL
	}
	fd, err := unix.Open(path, flags, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "run-lock")
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("invalid run lock: %s", path)
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func createRun() (*runDirectory, error) {
	if err := ensureRunsRoot(); err != nil {
		return nil, err
	}
	path, err := os.MkdirTemp(runsRoot, "run-")
	if err != nil {
		return nil, err
	}
	lock, err := openRunLock(filepath.Join(path, "lock"), true)
	if err != nil {
		os.RemoveAll(path)
		return nil, err
	}
	return &runDirectory{path: path, lock: lock}, nil
}

func (run *runDirectory) record(group string) error {
	data, err := json.Marshal(runMetadata{Cgroup: group})
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(run.path, ".state-")
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
	// Readers see either the previous complete record or the new complete record.
	return os.Rename(file.Name(), filepath.Join(run.path, "state.json"))
}

func (run *runDirectory) remove() error {
	defer run.lock.Close()
	if run.keep {
		return nil
	}
	if err := validateRunPath(runsRoot, run.path); err != nil {
		return err
	}
	if err := secureDirectory(run.path); err != nil {
		return err
	}
	return os.RemoveAll(run.path)
}

func recoverRuns(ctx context.Context, stderr io.Writer) error {
	if err := ensureRunsRoot(); err != nil {
		return err
	}
	entries, err := os.ReadDir(runsRoot)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !strings.HasPrefix(entry.Name(), "run-") {
			continue
		}
		path := filepath.Join(runsRoot, entry.Name())
		if err := secureDirectory(path); err != nil {
			// Another supervisor may have removed its completed run since ReadDir.
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return err
		}
		lock, err := openRunLock(filepath.Join(path, "lock"), false)
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			fmt.Fprintf(stderr, "mini-docker: leave unrecognized run directory %s: %v\n", path, err)
			continue
		}
		if err := reclaimRun(path); err != nil {
			fmt.Fprintf(stderr, "mini-docker: preserve stale run %s: %v\n", path, err)
		}
		lock.Close()
	}
	return nil
}

func reclaimRun(path string) error {
	return reclaimRunAt(path, statePaths{
		runsRoot: runsRoot, cgroupRoot: "/sys/fs/cgroup", mountInfo: "/proc/self/mountinfo",
		checkDirectory: secureDirectory,
	})
}

type statePaths struct {
	runsRoot, cgroupRoot, mountInfo string
	checkDirectory                  func(string) error
}

func validateRunPath(root, path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || filepath.Dir(path) != root ||
		!strings.HasPrefix(filepath.Base(path), "run-") || len(filepath.Base(path)) <= len("run-") {
		return fmt.Errorf("refuse cleanup outside a direct run directory: %s", path)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("refuse cleanup of a symlink or non-directory: %s", path)
	}
	return nil
}

// Dependencies are explicit so recovery can be tested without host mutations.
func reclaimRunAt(path string, paths statePaths) error {
	if err := validateRunPath(paths.runsRoot, path); err != nil {
		return err
	}
	if err := paths.checkDirectory(path); err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(path, "state.json"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil {
		var metadata runMetadata
		if err := json.Unmarshal(data, &metadata); err != nil {
			return err
		}
		group := filepath.Clean(metadata.Cgroup)
		if group != metadata.Cgroup || !strings.HasPrefix(group, paths.cgroupRoot+"/") ||
			filepath.Dir(group) == paths.cgroupRoot || !strings.HasPrefix(filepath.Base(group), "container-") ||
			len(filepath.Base(group)) <= len("container-") {
			return errors.New("invalid recorded cgroup path")
		}
		resolved, err := filepath.EvalSymlinks(group)
		if err == nil && resolved != group {
			return errors.New("recorded cgroup path contains a symlink")
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		events, err := os.ReadFile(filepath.Join(group, "cgroup.events"))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err == nil {
			empty, err := cgroupEmpty(string(events))
			if err != nil {
				return err
			}
			if !empty {
				return errors.New("recorded cgroup still contains processes")
			}
			if err := os.Remove(group); err != nil {
				return fmt.Errorf("remove recorded cgroup: %w", err)
			}
		}
	}
	data, err = os.ReadFile(paths.mountInfo)
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		for _, field := range []string{fields[3], fields[4]} {
			unescaped := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`).Replace(field)
			if unescaped == path || strings.HasPrefix(unescaped, path+"/") {
				return errors.New("runtime directory is referenced by a mount")
			}
		}
	}
	return os.RemoveAll(path)
}

func cgroupEmpty(events string) (bool, error) {
	found, empty := false, false
	for _, line := range strings.Split(events, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 2 {
			return false, errors.New("invalid recorded cgroup events")
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return false, errors.New("invalid recorded cgroup counter")
		}
		if fields[0] == "populated" {
			if found || value > 1 {
				return false, errors.New("invalid populated counter")
			}
			found, empty = true, value == 0
		}
	}
	if !found {
		return false, errors.New("missing populated counter")
	}
	return empty, nil
}
