//go:build linux

package integration

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func assertCgroupRemoved(t *testing.T, group string) {
	t.Helper()
	// systemd/kernel cgroup removal can finish after the unit becomes inactive.
	// Keep the cleanup guarantee while allowing that asynchronous completion.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(group); os.IsNotExist(err) {
			return
		} else if err != nil {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	events, _ := os.ReadFile(filepath.Join(group, "cgroup.events"))
	t.Fatalf("cgroup retained after completion: %s: %s", group, events)
}
