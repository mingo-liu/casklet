//go:build darwin

package macos

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/mingo-liu/casklet/internal/container"
	"github.com/mingo-liu/casklet/internal/project"
)

func macProjectConfig(t *testing.T, name string, ready bool) string {
	t.Helper()
	probe := "wget -T 1 -qO- http://127.0.0.1:8080"
	if !ready {
		probe = "exit 1"
	}
	document := map[string]any{
		"name": name, "volumes": []string{"data"},
		"services": map[string]any{
			"db":  map[string]any{"run": map[string]any{"mount": []string{"type=volume,source=data,target=/data"}, "health-cmd": probe, "health-interval": "100ms", "health-timeout": "1s", "command": []string{"/bin/sh", "-c", "mkdir -p /srv; printf ready > /srv/index.html; exec httpd -f -p 8080 -h /srv"}}},
			"api": map[string]any{"depends-on": map[string]string{"db": "healthy"}, "run": map[string]any{"command": []string{"/bin/sleep", "300"}}},
		},
	}
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(hostDirectory(t), "project.json")
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestProjectDeploymentReadinessReuseAndVolumeRetention(t *testing.T) {
	client(t)
	name := fmt.Sprintf("deploy-%x", time.Now().UnixNano())
	file := macProjectConfig(t, name, true)
	t.Cleanup(func() { command(t, "project", "down", "--volumes", name) })
	var first []container.Record
	if err := json.Unmarshal([]byte(success(t, "project", "up", "--config", file, "--json")), &first); err != nil || len(first) != 2 {
		t.Fatalf("up %v %v", first, err)
	}
	var metrics []container.Statistics
	if err := json.Unmarshal([]byte(success(t, "stats", "--json", "--interval", "100ms", "--filter", "label="+project.LabelName+"="+name)), &metrics); err != nil || len(metrics) != 2 || metrics[0].MemoryBytes == nil || metrics[1].CPUPercent == nil {
		t.Fatalf("project resource monitoring %v %v", metrics, err)
	}
	var db string
	for _, record := range first {
		if record.Labels[project.LabelService] == "db" {
			db = record.ID
		}
	}
	if db == "" {
		t.Fatal("no db service")
	}
	success(t, "exec", db, "--", "/bin/sh", "-c", "printf saved > /data/message")
	var second []container.Record
	if err := json.Unmarshal([]byte(success(t, "project", "up", "--config", file, "--json")), &second); err != nil || len(second) != 2 || second[0].ID != first[0].ID || second[1].ID != first[1].ID {
		t.Fatalf("duplicate deployment %v %v", second, err)
	}
	api := "cp-" + name + "-api"
	if output := success(t, "exec", api, "--", "wget", "-T", "1", "-qO-", "http://db:8080"); output != "ready" {
		t.Fatalf("scoped DNS %q", output)
	}
	success(t, "stop", db)
	success(t, "project", "up", "--config", file)
	success(t, "exec", db, "--", "/bin/true")
	// Drift must be rejected before touching either existing service.
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var changed map[string]any
	if err := json.Unmarshal(data, &changed); err != nil {
		t.Fatal(err)
	}
	services := changed["services"].(map[string]any)
	services["api"].(map[string]any)["run"].(map[string]any)["memory"] = "256m"
	drift, _ := json.Marshal(changed)
	os.WriteFile(file, drift, 0600)
	if _, _, code := command(t, "project", "up", "--config", file); code != 125 {
		t.Fatalf("accepted drift: %d", code)
	}
	success(t, "exec", api, "--", "/bin/true")
	os.WriteFile(file, data, 0600)
	success(t, "project", "down", name)
	success(t, "volume", "inspect", "cp-"+name+"-data")
	if output := success(t, "project", "ps", "--json", name); output != "[]\n" {
		t.Fatalf("down left containers %q", output)
	}
	success(t, "project", "up", "--config", file)
	if output := success(t, "exec", "cp-"+name+"-db", "--", "cat", "/data/message"); output != "saved" {
		t.Fatalf("volume data lost %q", output)
	}
	os.Remove(file)
	success(t, "project", "down", "--volumes", name)
	if _, _, code := command(t, "volume", "inspect", "cp-"+name+"-data"); code != 125 {
		t.Fatalf("explicit removal: %d", code)
	}
}

func TestProjectHealthFailureRollsBackThroughMacClient(t *testing.T) {
	client(t)
	name := fmt.Sprintf("rollback-%x", time.Now().UnixNano())
	file := macProjectConfig(t, name, false)
	t.Cleanup(func() { command(t, "project", "down", "--volumes", name) })
	unrelated := detached(t, "--", "/bin/sleep", "300")
	if output, _, code := command(t, "project", "up", "--config", file, "--timeout", "300ms"); code != 125 || output != "" {
		t.Fatalf("failed up: %d %q", code, output)
	}
	if output := success(t, "project", "ps", "--json", name); output != "[]\n" {
		t.Fatalf("rollback left containers %q", output)
	}
	for _, resource := range []struct{ kind, name string }{{"network", "default"}, {"volume", "data"}} {
		if _, _, code := command(t, resource.kind, "inspect", "cp-"+name+"-"+resource.name); code != 125 {
			t.Fatalf("rollback retained %s", resource.kind)
		}
	}
	success(t, "exec", unrelated, "--", "/bin/true")
}

func TestProjectCancellationCleansOnlyNewResources(t *testing.T) {
	binary := client(t)
	name := fmt.Sprintf("cancel-%x", time.Now().UnixNano())
	file := macProjectConfig(t, name, false)
	t.Cleanup(func() { command(t, "project", "down", "--volumes", name) })
	cmd := exec.Command(binary, "project", "up", "--config", file, "--timeout", "30s")
	var output, diagnostic bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &diagnostic
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	finished := false
	t.Cleanup(func() {
		if !finished {
			cmd.Process.Kill()
			<-done
		}
	})
	ready := false
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var records []container.Record
		data, _, code := command(t, "project", "ps", "--json", name)
		if code == 0 && json.Unmarshal([]byte(data), &records) == nil && len(records) == 1 && records[0].State == container.StateRunning {
			ready = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !ready {
		t.Fatal("project did not reach health wait")
	}
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		finished = true
		if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 130 {
			t.Fatalf("project cancel %v %s", err, diagnostic.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatal("project cancellation did not finish cleanup")
	}
	if output.Len() != 0 {
		t.Fatalf("canceled deployment output %q", output.String())
	}
	if data := success(t, "project", "ps", "--json", name); data != "[]\n" {
		t.Fatalf("cancellation leaked containers %s", data)
	}
	for _, kind := range []string{"network", "volume"} {
		logical := "default"
		if kind == "volume" {
			logical = "data"
		}
		if _, _, code := command(t, kind, "inspect", "cp-"+name+"-"+logical); code != 125 {
			t.Fatalf("cancellation leaked %s", kind)
		}
	}
}
