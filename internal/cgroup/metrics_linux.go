//go:build linux

package cgroup

import (
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// MetricsReader pins one cgroup inode across samples, including concurrent deletion.
// Opening never follows symlinks or reads ordinary host files.
type MetricsReader struct{ dir *os.File }

type Metrics struct {
	At           time.Time
	MemoryBytes  *uint64
	CPUUsageUsec *uint64
}

func OpenMetrics(path string) (*MetricsReader, error) {
	fd, err := unix.Openat2(unix.AT_FDCWD, path, &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_NO_SYMLINKS,
	})
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "cgroup-metrics")
	var fs unix.Statfs_t
	if err := unix.Fstatfs(fd, &fs); err != nil || fs.Type != unix.CGROUP2_SUPER_MAGIC {
		file.Close()
		return nil, errors.New("metrics require a cgroups v2 directory")
	}
	return &MetricsReader{dir: file}, nil
}

func (reader *MetricsReader) Close() error { return reader.dir.Close() }

func (reader *MetricsReader) read(name string) (string, error) {
	fd, err := unix.Openat(int(reader.dir.Fd()), name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return "", err
	}
	file := os.NewFile(uintptr(fd), "cgroup-counter")
	defer file.Close()
	stat, err := file.Stat()
	if err != nil || !stat.Mode().IsRegular() {
		return "", errors.New("invalid metric file")
	}
	const limit = 64 * 1024
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return "", err
	}
	if len(data) > limit {
		return "", errors.New("metric exceeds size limit")
	}
	return string(data), nil
}

// Sample keeps independent metric failures separate; valid zero counters are available.
func (reader *MetricsReader) Sample() Metrics {
	result := Metrics{}
	if text, err := reader.read("memory.current"); err == nil {
		result.MemoryBytes = parseMemoryMetric(text)
	}
	if text, err := reader.read("cpu.stat"); err == nil {
		if counters, err := parseCounters(text); err == nil {
			if value, ok := counters["usage_usec"]; ok {
				result.CPUUsageUsec = &value
			}
		}
	}
	result.At = time.Now()
	return result
}

func parseMemoryMetric(text string) *uint64 {
	text = strings.TrimSpace(text)
	if text == "" || strings.IndexFunc(text, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
		return nil
	}
	value, err := strconv.ParseUint(text, 10, 64)
	if err != nil {
		return nil
	}
	return &value
}
