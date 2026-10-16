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
)

func TestProjectLogsSelectionFollowAndCancellation(t *testing.T) {
	client(t)
	success(t, "doctor")
	name := fmt.Sprintf("logs-%x", time.Now().UnixNano())
	file := filepath.Join(t.TempDir(), "project.json")
	services := map[string]any{}
	for _, key := range []string{"api", "db"} {
		services[key] = map[string]any{"run": map[string]any{"network": "none", "command": []string{"/bin/sh", "-c", "echo first; echo second; sleep 300"}}}
	}
	data, _ := json.Marshal(map[string]any{"name": name, "networks": []string{}, "services": services})
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { success(t, "project", "down", name) })
	success(t, "project", "up", "--config", file)
	output := success(t, "project", "logs", "--tail", "1", name)
	if !strings.Contains(output, "[api] second\n") || !strings.Contains(output, "[db] second\n") || strings.Contains(output, "first") {
		t.Fatalf("tail: %q", output)
	}
	output = success(t, "project", "logs", "--service", "api", name)
	if strings.Contains(output, "[db]") || !strings.Contains(output, "[api] first\n") {
		t.Fatalf("selection: %q", output)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, client(t), "project", "logs", "--follow", "--service", "api", name)
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var diagnostic bytes.Buffer
	cmd.Stderr = &diagnostic
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	ready := make(chan string, 1)
	go func() { buf := make([]byte, 4096); n, _ := pipe.Read(buf); ready <- string(buf[:n]) }()
	select {
	case out := <-ready:
		if !strings.Contains(out, "[api]") {
			t.Fatalf("follow: %q", out)
		}
	case <-ctx.Done():
		t.Fatal("follow produced no output")
	}
	// Read-only followers must not retain the project lock.
	success(t, "project", "up", "--config", file)
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	err = cmd.Wait()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 143 || ctx.Err() != nil {
		t.Fatalf("cancel: %v %s", err, diagnostic.String())
	}
	if !strings.Contains(success(t, "project", "ps", name), "running") {
		t.Fatal("cancellation stopped workloads")
	}
}
