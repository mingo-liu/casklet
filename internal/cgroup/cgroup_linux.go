//go:build linux

package cgroup

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

const mountRoot = "/sys/fs/cgroup"

var creationMu sync.Mutex

// Check validates a writable, exclusively owned systemd delegation without
// modifying it. Launch the CLI through the delegated Linux scope script.
func Check() error {
	_, err := delegation(0)
	return err
}

func delegation(cpuQuota int64) (string, error) {
	if os.Geteuid() != 0 {
		return "", errors.New("cgroup management requires root")
	}
	var fs unix.Statfs_t
	if err := unix.Statfs(mountRoot, &fs); err != nil {
		return "", fmt.Errorf("inspect cgroup mount: %w", err)
	}
	if fs.Type != unix.CGROUP2_SUPER_MAGIC {
		return "", errors.New("/sys/fs/cgroup must be a unified cgroups v2 mount")
	}
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return "", fmt.Errorf("read cgroup membership: %w", err)
	}
	relative, err := unifiedPath(string(data))
	if err != nil {
		return "", err
	}
	dir := filepath.Join(mountRoot, relative)
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil || resolved != dir {
		return "", fmt.Errorf("cgroup path must exist without symlinks: %s", dir)
	}
	marker := make([]byte, 32)
	n, err := unix.Getxattr(dir, "user.delegate", marker)
	if err != nil || string(marker[:n]) != "1" {
		return "", fmt.Errorf("cgroup %s is not marked user.delegate=1; use the delegated Linux launcher", dir)
	}
	for _, name := range []string{"cgroup.controllers", "cgroup.type", "cgroup.procs"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			return "", fmt.Errorf("missing cgroups v2 control %s: %w", name, err)
		}
	}
	controllers, err := os.ReadFile(filepath.Join(dir, "cgroup.controllers"))
	if err != nil {
		return "", err
	}
	if err := checkControllers(string(controllers), cpuQuota); err != nil {
		return "", err
	}
	kind, err := os.ReadFile(filepath.Join(dir, "cgroup.type"))
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(string(kind)) != "domain" {
		return "", errors.New("delegated cgroup must be a domain cgroup")
	}
	procs, err := os.ReadFile(filepath.Join(dir, "cgroup.procs"))
	if err != nil {
		return "", err
	}
	if err := exclusiveProcess(string(procs), os.Getpid()); err != nil {
		return "", err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			return "", errors.New("delegated scope must be fresh and contain no child cgroups")
		}
	}
	controls := []string{".", "cgroup.procs", "cgroup.subtree_control", "cgroup.kill", "memory.max", "memory.swap.max", "memory.oom.group", "pids.max"}
	if cpuQuota > 0 {
		controls = append(controls, "cpu.max")
	}
	for _, name := range controls {
		control := filepath.Join(dir, name)
		if err := unix.Access(control, unix.W_OK); err != nil {
			return "", fmt.Errorf("required cgroup control is unavailable or not writable: %s: %w", control, err)
		}
	}
	return dir, nil
}

// Create moves the supervisor to a manager leaf and creates one limited
// workload leaf. Use a new delegated scope for every invocation.
func Create(memory, pids, cpuQuota int64) (*Group, error) {
	creationMu.Lock()
	defer creationMu.Unlock()
	if err := validateLimits(memory, pids, cpuQuota); err != nil {
		return nil, err
	}
	dir, err := delegation(cpuQuota)
	if err != nil {
		return nil, err
	}
	manager := filepath.Join(dir, "manager")
	if err := os.Mkdir(manager, 0700); err != nil {
		return nil, fmt.Errorf("create manager cgroup: %w", err)
	}
	// Writing a PID to cgroup.procs migrates every thread, including Go threads.
	if err := writeControl(filepath.Join(manager, "cgroup.procs"), strconv.Itoa(os.Getpid())); err != nil {
		return nil, errors.Join(err, os.Remove(manager))
	}
	// Leave the populated manager leaf to systemd's scope teardown on failure.
	procs, err := os.ReadFile(filepath.Join(dir, "cgroup.procs"))
	if err != nil {
		return nil, fmt.Errorf("inspect delegation after migration: %w", err)
	}
	if len(strings.Fields(string(procs))) != 0 {
		return nil, errors.New("delegated cgroup still contains processes after supervisor migration")
	}
	controllers := requiredControllers(cpuQuota)
	if err := writeControl(filepath.Join(dir, "cgroup.subtree_control"), "+"+strings.Join(controllers, " +")); err != nil {
		return nil, err
	}
	var id [12]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, fmt.Errorf("generate cgroup ID: %w", err)
	}
	g := &Group{path: filepath.Join(dir, "container-"+hex.EncodeToString(id[:]))}
	if err := os.Mkdir(g.path, 0700); err != nil {
		return nil, fmt.Errorf("create workload cgroup: %w", err)
	}
	if err := configureLimits(g.path, memory, pids, cpuQuota, writeControl); err != nil {
		return nil, errors.Join(err, g.Close())
	}
	if err := unix.Access(filepath.Join(g.path, "cgroup.kill"), unix.W_OK); err != nil {
		return nil, errors.Join(fmt.Errorf("workload requires writable cgroup.kill: %w", err), g.Close())
	}
	return g, nil
}
