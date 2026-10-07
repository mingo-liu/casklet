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
	"time"

	"github.com/mingo-liu/mini-docker/internal/cgroup"
	"github.com/mingo-liu/mini-docker/internal/network"

	"golang.org/x/sys/unix"
)

const (
	runsRoot      = "/var/lib/mini-docker/runs"
	maxStateBytes = 4096
)

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

func createRun(ctx context.Context) (*runDirectory, error) {
	if err := ensureRunsRoot(); err != nil {
		return nil, err
	}
	return createRunAt(ctx, runsRoot)
}

func createRunAt(ctx context.Context, root string) (*runDirectory, error) {
	coordination, err := acquireStateLock(ctx, root, false)
	if err != nil {
		return nil, err
	}
	defer coordination.Close()
	// Recovery cannot inspect a new directory until its own lock is held.
	path, err := os.MkdirTemp(root, "run-")
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
	if err := network.Cleanup(run.path); err != nil {
		return fmt.Errorf("network cleanup: %w", err)
	}
	return os.RemoveAll(run.path)
}

func recoverRuns(ctx context.Context, stderr io.Writer) error {
	if err := ensureRunsRoot(); err != nil {
		return err
	}
	return recoverRunsAt(ctx, stderr, productionStatePaths())
}

func recoverRunsAt(ctx context.Context, stderr io.Writer, paths statePaths) error {
	entries, err := os.ReadDir(paths.runsRoot)
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
		path := filepath.Join(paths.runsRoot, entry.Name())
		lock, err := lockRecoveryCandidate(ctx, path, paths)
		if err != nil {
			// Another supervisor may have removed its completed run since ReadDir.
			if errors.Is(err, os.ErrNotExist) || errors.Is(err, unix.EWOULDBLOCK) {
				continue
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			fmt.Fprintf(stderr, "mini-docker: leave unrecognized run directory %s: %v\n", path, err)
			continue
		}
		if err := reclaimRunAt(path, paths); err != nil {
			fmt.Fprintf(stderr, "mini-docker: preserve stale run %s: %v\n", path, err)
		}
		lock.Close()
	}
	return nil
}

func productionStatePaths() statePaths {
	return statePaths{
		runsRoot: runsRoot, cgroupRoot: "/sys/fs/cgroup", mountInfo: "/proc/self/mountinfo",
		checkDirectory: secureDirectory,
	}
}

// RecoverAbandoned honors run locks and reclaims only verified stale resources.
func RecoverAbandoned(ctx context.Context, stderr io.Writer) error {
	return recoverRuns(ctx, stderr)
}

// RecoverRun reclaims only an abandoned foreground working directory. A live
// init retains its run lock, so background management cannot remove its rootfs.
func RecoverRun(ctx context.Context, path string) error {
	paths := productionStatePaths()
	if err := validateRunPath(paths.runsRoot, path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := ensureRunsRoot(); err != nil {
		return err
	}
	lock, err := lockRecoveryCandidate(ctx, path, paths)
	if err != nil {
		return fmt.Errorf("lock abandoned container resources: %w", err)
	}
	defer lock.Close()
	return reclaimRunAt(path, paths)
}

type statePaths struct {
	runsRoot, cgroupRoot, mountInfo string
	checkDirectory                  func(string) error
}

func lockRecoveryCandidate(ctx context.Context, path string, paths statePaths) (*os.File, error) {
	coordination, err := acquireStateLock(ctx, paths.runsRoot, true)
	if err != nil {
		return nil, err
	}
	defer coordination.Close()
	if err := paths.checkDirectory(path); err != nil {
		return nil, err
	}
	return openRunLock(filepath.Join(path, "lock"), false)
}

// The persistent coordination lock covers publication and candidate locking,
// never rootfs copying, command execution, or recursive cleanup.
func acquireStateLock(ctx context.Context, root string, shared bool) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path := filepath.Join(root, ".lock")
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "state-coordination")
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		file.Close()
		return nil, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0022 != 0 || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		file.Close()
		return nil, errors.New("state coordination lock must be a singly linked regular file owned by the current user without group or other write permissions")
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
		err := unix.Flock(fd, operation|unix.LOCK_NB)
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
	data, err := readRunState(filepath.Join(path, "state.json"))
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
			if err := cgroup.RemoveEmpty(group); err != nil {
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
	if paths.runsRoot == runsRoot {
		if err := network.Cleanup(path); err != nil {
			return fmt.Errorf("recover network: %w", err)
		}
	}
	return os.RemoveAll(path)
}

func readRunState(path string) ([]byte, error) {
	// A malformed stale artifact must not follow a link, wait for a FIFO writer,
	// or allocate arbitrary memory before another container can start.
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "run-state")
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("run state must be a regular file")
	}
	if info.Size() > maxStateBytes {
		return nil, errors.New("run state exceeds the size limit")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxStateBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxStateBytes {
		return nil, errors.New("run state exceeds the size limit")
	}
	return data, nil
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
