//go:build linux

package cgroup

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// NewChild adds a cleanup boundary beneath the aggregate workload limits.
// No controllers are enabled here, so the parent's populated leaf can retain
// init and the main command while every descendant remains subject to its limits.
func (g *Group) NewChild() (*Group, error) {
	var fs unix.Statfs_t
	if err := unix.Statfs(g.path, &fs); err != nil || fs.Type != unix.CGROUP2_SUPER_MAGIC {
		return nil, fmt.Errorf("exec requires a cgroups v2 workload: %s", g.path)
	}
	var id [12]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, err
	}
	child := &Group{path: filepath.Join(g.path, "exec-"+hex.EncodeToString(id[:]))}
	if err := os.Mkdir(child.path, 0700); err != nil {
		return nil, fmt.Errorf("create exec cgroup: %w", err)
	}
	if err := unix.Access(filepath.Join(child.path, "cgroup.kill"), unix.W_OK); err != nil {
		_ = child.Close()
		return nil, fmt.Errorf("exec cgroup requires cgroup.kill: %w", err)
	}
	return child, nil
}
