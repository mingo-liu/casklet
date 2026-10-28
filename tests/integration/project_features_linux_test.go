//go:build linux

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mingo-liu/casklet/internal/container"
)

func linuxTaskConfig(t *testing.T, name, task string) string {
	t.Helper()
	doc := map[string]any{"name": name, "volumes": []string{"data"}, "services": map[string]any{
		"migrate": map[string]any{"task": true, "run": map[string]any{"rootfs": template, "mount": []string{"type=volume,source=data,target=/data"}, "command": []string{"/bin/sh", "-c", task}}},
		"api":     map[string]any{"depends-on": map[string]string{"migrate": "completed"}, "run": map[string]any{"rootfs": template, "env": []string{"PRIVATE=event-secret"}, "mount": []string{"type=volume,source=data,target=/data"}, "command": []string{"/bin/sh", "-c", "test -f /data/count || exit 23; exec sleep 300"}}},
	}}
	data, _ := json.Marshal(doc)
	filename := filepath.Join(t.TempDir(), "tasks.json")
	if err := os.WriteFile(filename, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { backgroundCLI(t, "project", "down", "--volumes", name) })
	return filename
}
func TestProjectOperationsTasksAndEventsThroughLinux(t *testing.T) {
	require(t)
	t.Setenv("CASKLET_MACHINE_STATE_DIR", t.TempDir())
	name := fmt.Sprintf("tasks-%x", time.Now().UnixNano())
	file := linuxTaskConfig(t, name, "if [ -f /data/fail ]; then exit 9; fi; n=$(cat /data/count 2>/dev/null || echo 0); echo $((n+1)) > /data/count")
	backgroundSuccess(t, "project", "up", "--config", file, "--wait", "--timeout", "10s")
	if out := backgroundSuccess(t, "project", "exec", name, "api", "--", "cat", "/data/count"); out != "1\n" {
		t.Fatal(out)
	}
	backgroundSuccess(t, "project", "up", "--config", file, "--timeout", "10s")
	backgroundSuccess(t, "project", "restart", name)
	if out := backgroundSuccess(t, "project", "exec", name, "api", "--", "cat", "/data/count"); out != "1\n" {
		t.Fatal("default restart reran task")
	}
	backgroundSuccess(t, "project", "stop", name, "api")
	backgroundSuccess(t, "project", "start", name, "api")
	backgroundSuccess(t, "project", "exec", name, "api", "--", "touch", "/data/fail")
	backgroundSuccess(t, "project", "restart", name, "migrate")
	backgroundCLI(t, "wait", "cp-"+name+"-migrate")
	if code, _, _ := backgroundCLI(t, "project", "up", "--config", file, "--timeout", "10s"); code != 9 {
		t.Fatalf("existing failed task status %d", code)
	}
	backgroundSuccess(t, "project", "exec", name, "api", "--", "rm", "/data/fail")
	backgroundSuccess(t, "project", "restart", name, "migrate")
	backgroundSuccess(t, "project", "up", "--config", file, "--timeout", "10s")
	if out := backgroundSuccess(t, "project", "exec", name, "api", "--", "cat", "/data/count"); out != "2\n" {
		t.Fatal(out)
	}
	output := backgroundSuccess(t, "events", "--json", "--filter", "project="+name)
	if strings.Contains(output, "event-secret") {
		t.Fatal("events leaked configuration")
	}
	decoder := json.NewDecoder(strings.NewReader(output))
	var last uint64
	seen := map[string]bool{}
	for decoder.More() {
		var e container.Event
		if err := decoder.Decode(&e); err != nil {
			t.Fatal(err)
		}
		if e.Sequence <= last {
			t.Fatal("unordered events")
		}
		last = e.Sequence
		seen[e.Type] = true
	}
	for _, kind := range []string{"create", "start", "exit", "restart"} {
		if !seen[kind] {
			t.Fatal("missing event " + kind)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "events", "--follow", "--json", "--after", fmt.Sprint(last), "--filter", "project="+name, "--filter", "event=start")
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var diagnostic bytes.Buffer
	cmd.Stderr = &diagnostic
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	observed := make(chan error, 1)
	go func() { var e container.Event; observed <- json.NewDecoder(pipe).Decode(&e) }()
	backgroundSuccess(t, "project", "restart", name, "api")
	select {
	case err := <-observed:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("no event")
	}
	cmd.Process.Signal(syscall.SIGTERM)
	if err := cmd.Wait(); err == nil {
		t.Fatal("canceled event follower succeeded")
	}
	backgroundSuccess(t, "project", "exec", name, "api", "--", "true")
}
func TestProjectTaskFailureAndDeadlineLinux(t *testing.T) {
	require(t)
	t.Setenv("CASKLET_MACHINE_STATE_DIR", t.TempDir())
	for _, tc := range []struct {
		command, timeout string
		code             int
	}{{"exit 7", "10s", 7}, {"sleep 300", "300ms", 124}} {
		name := fmt.Sprintf("task-fail-%x", time.Now().UnixNano())
		file := linuxTaskConfig(t, name, tc.command)
		code, _, stderr := backgroundCLI(t, "project", "up", "--config", file, "--timeout", tc.timeout)
		if code != tc.code {
			t.Fatalf("task code %d expected %d: %s", code, tc.code, stderr)
		}
		if out := backgroundSuccess(t, "project", "ps", "--json", name); out != "[]\n" {
			t.Fatalf("task rollback %s", out)
		}
	}
}

func TestEventJournalFailureDoesNotBlockStopOrRemoval(t *testing.T) {
	require(t)
	id := startBackground(t, backgroundName(t), nil, "/bin/sleep", "300")
	journal := "/var/lib/casklet/containers/.events.json"
	marker := "/var/lib/casklet/containers/.events-error"
	saved, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		temporary := journal + ".test-restore"
		if err := os.WriteFile(temporary, saved, 0600); err != nil {
			t.Error(err)
			return
		}
		if err := os.Rename(temporary, journal); err != nil {
			t.Error(err)
		}
		if err := os.Remove(marker); err != nil && !os.IsNotExist(err) {
			t.Error(err)
		}
	})
	if err := os.WriteFile(journal, []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := backgroundCLI(t, "stop", id); code != 0 || !strings.Contains(stderr, "event recording failed") {
		t.Fatalf("stop blocked by observation failure: %d %s", code, stderr)
	}
	backgroundSuccess(t, "rm", id)
	if code, _, stderr := backgroundCLI(t, "events", "--json"); code != 125 || !strings.Contains(stderr, "history is incomplete") {
		t.Fatalf("missing event failure marker: %d %s", code, stderr)
	}
	if _, err := os.Stat(filepath.Join("/var/lib/casklet/containers", id)); !os.IsNotExist(err) {
		t.Fatalf("container storage was not removed: %v", err)
	}
}
