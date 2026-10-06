// Package cgroup manages a single workload in a delegated cgroups v2 scope.
package cgroup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mingo-liu/mini-docker/internal/config"
)

// Group is a workload leaf. The supervisor must empty it before Close.
type Group struct {
	path string
}

// Path returns the host cgroup directory for state records.
func (g *Group) Path() string { return g.path }

// Add moves all threads of a process into the workload leaf.
func (g *Group) Add(pid int) error {
	if pid <= 0 {
		return errors.New("cgroup PID must be positive")
	}
	return writeControl(filepath.Join(g.path, "cgroup.procs"), strconv.Itoa(pid))
}

// Kill atomically kills all processes in the workload and its descendants.
func (g *Group) Kill() error {
	return writeControl(filepath.Join(g.path, "cgroup.kill"), "1")
}

// WaitEmpty waits until the kernel reports that no workload processes remain.
// The caller must provide a deadline to bound cleanup.
func (g *Group) WaitEmpty(ctx context.Context) error {
	if _, ok := ctx.Deadline(); !ok {
		return errors.New("cgroup cleanup requires a context deadline")
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("wait for empty cgroup %s: %w", g.path, err)
		}
		events, err := readCounters(filepath.Join(g.path, "cgroup.events"))
		if err != nil {
			return err
		}
		populated, ok := events["populated"]
		if !ok || populated > 1 {
			return fmt.Errorf("invalid populated counter in %s/cgroup.events", g.path)
		}
		if populated == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for empty cgroup %s: %w", g.path, ctx.Err())
		case <-ticker.C:
		}
	}
}

// OOMKilled reports whether the kernel killed any workload process for memory.
func (g *Group) OOMKilled() (bool, error) {
	events, err := readCounters(filepath.Join(g.path, "memory.events"))
	if err != nil {
		return false, err
	}
	killed, ok := events["oom_kill"]
	if !ok {
		return false, fmt.Errorf("missing oom_kill counter in %s/memory.events", g.path)
	}
	return killed > 0 || events["oom_group_kill"] > 0, nil
}

// Close removes the empty workload leaf. Repeated cleanup is harmless.
// The manager leaf is owned by the process and removed when systemd ends scope.
func (g *Group) Close() error {
	if err := os.Remove(g.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove cgroup %s: %w", g.path, err)
	}
	return nil
}

func writeControl(filename, value string) error {
	// Never create an ordinary file when a required kernel control is missing.
	f, err := os.OpenFile(filename, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return fmt.Errorf("open cgroup control %s: %w", filename, err)
	}
	_, writeErr := f.WriteString(value)
	closeErr := f.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return fmt.Errorf("write cgroup control %s: %w", filename, err)
	}
	return nil
}

func validateLimits(memory, pids, cpuQuota int64) error {
	if memory <= 0 || pids <= 0 {
		return errors.New("cgroup memory and PID limits must be positive")
	}
	// Bound quota independently of CLI parsing, including multiplication inside
	// the kernel. The project supports up to 1000 CPUs at a fixed 100ms period.
	if cpuQuota != 0 && (cpuQuota < config.MinCPUQuota || cpuQuota > config.MaxCPUQuota) {
		return errors.New("cgroup CPU quota must be zero or between 1000 and 100000000 microseconds")
	}
	return nil
}

func requiredControllers(cpuQuota int64) []string {
	controllers := []string{"memory", "pids"}
	if cpuQuota > 0 {
		controllers = append(controllers, "cpu")
	}
	return controllers
}

func checkControllers(data string, cpuQuota int64) error {
	available := make(map[string]bool)
	for _, controller := range strings.Fields(data) {
		available[controller] = true
	}
	for _, controller := range requiredControllers(cpuQuota) {
		if !available[controller] {
			return fmt.Errorf("delegated cgroup requires the %s controller", controller)
		}
	}
	return nil
}

func configureLimits(dir string, memory, pids, cpuQuota int64, write func(string, string) error) error {
	if err := validateLimits(memory, pids, cpuQuota); err != nil {
		return err
	}
	settings := []struct{ name, value string }{
		{"memory.max", strconv.FormatInt(memory, 10)},
		{"memory.swap.max", "0"},
		{"memory.oom.group", "1"},
		{"pids.max", strconv.FormatInt(pids, 10)},
	}
	if cpuQuota > 0 {
		settings = append(settings, struct{ name, value string }{"cpu.max", fmt.Sprintf("%d %d", cpuQuota, config.CPUPeriod)})
	}
	for _, setting := range settings {
		if err := write(filepath.Join(dir, setting.name), setting.value); err != nil {
			return err
		}
	}
	return nil
}

func readCounters(filename string) (map[string]uint64, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("read cgroup counters %s: %w", filename, err)
	}
	counters, err := parseCounters(string(data))
	if err != nil {
		return nil, fmt.Errorf("parse cgroup counters %s: %w", filename, err)
	}
	return counters, nil
}

func parseCounters(data string) (map[string]uint64, error) {
	counters := make(map[string]uint64)
	for _, line := range strings.Split(data, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, fmt.Errorf("invalid counter line %q", line)
		}
		if _, exists := counters[fields[0]]; exists {
			return nil, fmt.Errorf("duplicate counter %q", fields[0])
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid counter %q: %w", fields[0], err)
		}
		counters[fields[0]] = value
	}
	return counters, nil
}

func unifiedPath(data string) (string, error) {
	var found string
	for _, line := range strings.Split(strings.TrimSpace(data), "\n") {
		if !strings.HasPrefix(line, "0::") {
			continue
		}
		if found != "" {
			return "", errors.New("multiple unified cgroup memberships")
		}
		found = strings.TrimPrefix(line, "0::")
		if found == "" || !strings.HasPrefix(found, "/") || path.Clean(found) != found || found == "/" {
			return "", errors.New("a non-root absolute delegated cgroup path is required")
		}
	}
	if found == "" {
		return "", errors.New("unified cgroups v2 membership is required")
	}
	return found, nil
}

func exclusiveProcess(procs string, pid int) error {
	fields := strings.Fields(procs)
	if len(fields) != 1 || fields[0] != strconv.Itoa(pid) {
		return errors.New("delegated scope must contain only the CLI process; launch each invocation in its own scope")
	}
	return nil
}
