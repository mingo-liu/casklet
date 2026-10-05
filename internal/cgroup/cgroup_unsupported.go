//go:build !linux

package cgroup

import "errors"

// Check validates prerequisites for creating a workload cgroup.
func Check() error { return errors.New("cgroups v2 require Linux") }

// Create configures a workload cgroup before its init process starts executing.
func Create(memory, pids int64) (*Group, error) {
	return nil, errors.New("cgroups v2 require Linux")
}
