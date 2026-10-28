//go:build darwin

package macos

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
	"github.com/mingo-liu/casklet/internal/project"
	"golang.org/x/sys/unix"
)

func macTaskConfig(t *testing.T, name, task string) string {
	t.Helper()
	doc := map[string]any{"name": name, "volumes": []string{"data"}, "services": map[string]any{
		"migrate": map[string]any{"task": true, "run": map[string]any{"mount": []string{"type=volume,source=data,target=/data"}, "command": []string{"/bin/sh", "-c", task}}},
		"api":     map[string]any{"depends-on": map[string]string{"migrate": "completed"}, "run": map[string]any{"env": []string{"PRIVATE=must-not-appear-in-events"}, "mount": []string{"type=volume,source=data,target=/data,readonly"}, "health-cmd": "test -f /data/count", "health-interval": "100ms", "command": []string{"/bin/sh", "-c", "test -f /data/count || exit 23; exec sleep 300"}}},
	}}
	data, _ := json.Marshal(doc)
	filename := filepath.Join(hostDirectory(t), "tasks.json")
	if err := os.WriteFile(filename, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { command(t, "project", "down", "--volumes", name) })
	return filename
}
func TestProjectOperationsTasksAndLifecycleEvents(t *testing.T) {
	client(t)
	name := fmt.Sprintf("tasks-%x", time.Now().UnixNano())
	file := macTaskConfig(t, name, "n=$(cat /data/count 2>/dev/null || echo 0); echo $((n+1)) > /data/count")
	var records []container.Record
	if err := json.Unmarshal([]byte(success(t, "project", "up", "--config", file, "--wait", "--timeout", "30s", "--json")), &records); err != nil {
		t.Fatal(err)
	}
	var job container.Record
	for _, r := range records {
		if r.Labels[project.LabelTask] == "true" {
			job = r
		}
	}
	if job.ID == "" || job.ExitCode == nil || *job.ExitCode != 0 || job.ExitReason != "exited" {
		t.Fatalf("task %+v", job)
	}
	if out := success(t, "project", "exec", name, "api", "--", "cat", "/data/count"); out != "1\n" {
		t.Fatal(out)
	}
	success(t, "project", "up", "--config", file, "--timeout", "30s")
	success(t, "project", "restart", name)
	if out := success(t, "project", "exec", "--env", "VALUE=exact value", name, "api", "--", "/bin/sh", "-c", "printf '%s' \"$VALUE\"; cat /data/count"); out != "exact value1\n" {
		t.Fatalf("exec %q", out)
	}
	if _, _, code := command(t, "project", "exec", name, "api", "--", "/bin/sh", "-c", "exit 7"); code != 7 {
		t.Fatalf("exec code %d", code)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	input := exec.CommandContext(ctx, client(t), "project", "exec", "-i", name, "api", "--", "cat")
	input.Stdin = strings.NewReader("exact input\x00")
	if out, err := input.Output(); err != nil || string(out) != "exact input\x00" {
		t.Fatalf("exec stdin %q %v", out, err)
	}
	success(t, "project", "stop", name, "api")
	success(t, "project", "start", name, "api")
	success(t, "project", "restart", name, "migrate")
	success(t, "project", "up", "--config", file, "--wait", "--timeout", "30s")
	if out := success(t, "project", "exec", name, "api", "--", "cat", "/data/count"); out != "2\n" {
		t.Fatalf("task rerun %q", out)
	}
	history := success(t, "events", "--json", "--filter", "project="+name)
	if strings.Contains(history, "must-not-appear-in-events") || strings.Contains(history, "/var/lib/casklet") {
		t.Fatal("private event fields leaked")
	}
	decoder := json.NewDecoder(strings.NewReader(history))
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
	for _, kind := range []string{"create", "start", "exit", "restart", "health"} {
		if !seen[kind] {
			t.Fatalf("missing event %s", kind)
		}
	}
	follow := exec.CommandContext(ctx, client(t), "events", "--json", "--follow", "--after", fmt.Sprint(last), "--filter", "project="+name, "--filter", "event=start")
	pipe, err := follow.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var diagnostic bytes.Buffer
	follow.Stderr = &diagnostic
	if err := follow.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { follow.Process.Kill(); follow.Wait() }()
	observed := make(chan error, 1)
	go func() {
		var e container.Event
		err := json.NewDecoder(pipe).Decode(&e)
		if err == nil && (e.Type != "start" || e.Project != name) {
			err = fmt.Errorf("unexpected event %+v", e)
		}
		observed <- err
	}()
	success(t, "project", "restart", name, "api")
	select {
	case err := <-observed:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("no followed event")
	}
	if err := follow.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	err = follow.Wait()
	exit, ok := err.(*exec.ExitError)
	if !ok || exit.ExitCode() != 143 {
		t.Fatalf("event cancel %v %s", err, &diagnostic)
	}
	success(t, "project", "exec", name, "api", "--", "true")
}
func TestProjectTaskFailureAndDeadlineThroughMac(t *testing.T) {
	client(t)
	for _, tc := range []struct {
		command, timeout string
		code             int
	}{{"exit 7", "30s", 7}, {"sleep 300", "1s", 124}} {
		name := fmt.Sprintf("task-fail-%x", time.Now().UnixNano())
		file := macTaskConfig(t, name, tc.command)
		_, stderr, code := command(t, "project", "up", "--config", file, "--timeout", tc.timeout)
		if code != tc.code {
			t.Fatalf("task status %d expected %d: %s", code, tc.code, stderr)
		}
		if out := success(t, "project", "ps", "--json", name); out != "[]\n" {
			t.Fatalf("task rollback %s", out)
		}
	}
}

func TestEventsCancelWithFullHostOutputPipe(t *testing.T) {
	client(t)
	id := detached(t, "--", "/bin/sleep", "300")
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	fd := int(writer.Fd())
	if err := unix.SetNonblock(fd, true); err != nil {
		t.Fatal(err)
	}
	block := make([]byte, 4096)
	for {
		_, err := unix.Write(fd, block)
		if err == unix.EAGAIN {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := unix.SetNonblock(fd, false); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, client(t), "events", "--follow", "--after", "0", "--json", "--filter", "container="+id)
	cmd.Stdout = writer
	var diagnostic bytes.Buffer
	cmd.Stderr = &diagnostic
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	time.Sleep(300 * time.Millisecond)
	success(t, "inspect", id)
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		exit, ok := err.(*exec.ExitError)
		if !ok || exit.ExitCode() != 143 {
			t.Fatalf("event cancellation %v %s", err, &diagnostic)
		}
	case <-time.After(5 * time.Second):
		cmd.Process.Kill()
		<-done
		t.Fatal("blocked event output prevented cancellation")
	}
	success(t, "exec", id, "--", "true")
}
