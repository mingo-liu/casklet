//go:build linux

package cgroup

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestMetricsFailuresAreIndependent(t *testing.T) {
	dir := t.TempDir()
	file, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	reader := &MetricsReader{dir: file}
	defer reader.Close()
	write := func(name, text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	check := func(memory, cpu bool) {
		t.Helper()
		got := reader.Sample()
		if (got.MemoryBytes != nil) != memory || (got.CPUUsageUsec != nil) != cpu || got.At.IsZero() {
			t.Fatalf("metrics: %+v", got)
		}
	}
	check(false, false)
	write("memory.current", "0\n")
	write("cpu.stat", "usage_usec 123\nuser_usec 100\nsystem_usec 23\n")
	check(true, true)
	for _, text := range []string{"", "-1", "+1", "max", "1 2", "18446744073709551616"} {
		write("memory.current", text)
		check(false, true)
	}
	write("memory.current", "4096\n")
	for _, text := range []string{"", "usage_usec -1", "usage_usec 1\nusage_usec 2", "user_usec 1", "usage_usec 18446744073709551616", "usage_usec 1 extra"} {
		write("cpu.stat", text)
		check(true, false)
	}
	if err := os.Remove(filepath.Join(dir, "cpu.stat")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("memory.current", filepath.Join(dir, "cpu.stat")); err != nil {
		t.Fatal(err)
	}
	check(true, false)
	if err := os.Remove(filepath.Join(dir, "cpu.stat")); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(dir, "cpu.stat"), 0600); err != nil {
		t.Fatal(err)
	}
	check(true, false)
}

func TestOpenMetricsRejectsHostFilesAndSymlinks(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{dir, link, filepath.Join(dir, "missing")} {
		reader, err := OpenMetrics(path)
		if err == nil {
			reader.Close()
			t.Fatalf("accepted unsafe metrics directory %q", path)
		}
	}
}

func TestMetricsReaderPinsOneDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "workload")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "memory.current"), []byte("123"), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	reader := &MetricsReader{dir: file}
	defer reader.Close()
	if err := os.Rename(dir, dir+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "memory.current"), []byte("999"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := reader.Sample(); got.MemoryBytes == nil || *got.MemoryBytes != 123 {
		t.Fatalf("reader switched workload: %+v", got)
	}
	if err := os.Remove(filepath.Join(dir+"-old", "memory.current")); err != nil {
		t.Fatal(err)
	}
	if got := reader.Sample(); got.MemoryBytes != nil {
		t.Fatalf("removed metric replaced by another workload: %+v", got)
	}
}
