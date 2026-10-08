//go:build darwin

package macos

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mingo-liu/casklet/internal/container"
)

func TestCommandHelp(t *testing.T) {
	for _, args := range [][]string{
		{"help"}, {"--help"}, {"-h"},
		{"run", "--help"}, {"doctor", "--help"}, {"exec", "--help"},
		{"ps", "--help"}, {"inspect", "--help"}, {"stats", "--help"},
		{"wait", "--help"}, {"start", "--help"}, {"restart", "--help"},
		{"stop", "--help"}, {"logs", "--help"}, {"rm", "--help"},
		{"image", "--help"}, {"image", "import", "--help"},
		{"image", "ls", "--help"}, {"image", "rm", "--help"},
		{"rootfs", "--help"}, {"machine", "--help"},
		{"machine", "init", "--cpus", "2", "--help"},
		{"machine", "start", "--help"}, {"machine", "stop", "--help"},
		{"machine", "status", "--help"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			out, diagnostic, code := command(t, args...)
			if code != 0 || !strings.Contains(out, "Usage:") || diagnostic != "" {
				t.Fatalf("help: exit=%d stdout=%q stderr=%q", code, out, diagnostic)
			}
		})
	}
}

func TestCommandArgumentErrors(t *testing.T) {
	for _, args := range [][]string{
		{"unknown"}, {"run"}, {"doctor", "unexpected"}, {"exec"},
		{"ps", "unexpected"}, {"inspect"}, {"stats"}, {"wait"},
		{"start"}, {"restart"}, {"stop"}, {"logs"}, {"rm"},
		{"image"}, {"image", "import"}, {"image", "ls", "unexpected"},
		{"image", "rm", "invalid"}, {"rootfs"}, {"machine"},
		{"machine", "init", "--cpus", "0"},
		{"machine", "start", "unexpected"}, {"machine", "stop", "unexpected"},
		{"machine", "status", "unexpected"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			out, diagnostic, code := command(t, args...)
			if code != 125 || out != "" || !strings.HasPrefix(diagnostic, "casklet: ") {
				t.Fatalf("argument error: exit=%d stdout=%q stderr=%q", code, out, diagnostic)
			}
		})
	}
}

func TestWaitAndContainerListings(t *testing.T) {
	name := fmt.Sprintf("macos-wait-%d", time.Now().UnixNano())
	id := detached(t, "--name", name, "--", "/bin/sh", "-c", "echo wait-ready; while [ ! -e /finish ]; do sleep 0.1; done; exit 7")
	var records []container.Record
	if err := json.Unmarshal([]byte(success(t, "ps", "--json")), &records); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, record := range records {
		if record.ID == id && record.Name == name && record.State == container.StateRunning {
			found = true
		}
	}
	if !found {
		t.Fatalf("running container missing from ps: %+v", records)
	}
	if out := success(t, "ps"); !strings.Contains(out, name) {
		t.Fatalf("table listing omitted name: %q", out)
	}
	success(t, "exec", name, "--", "/bin/touch", "/finish")
	for i := 0; i < 2; i++ {
		out, diagnostic, code := command(t, "wait", name)
		if code != 7 || out != "7\n" || diagnostic != "" {
			t.Fatalf("wait: exit=%d stdout=%q stderr=%q", code, out, diagnostic)
		}
	}
	var inspection container.Inspection
	if err := json.Unmarshal([]byte(success(t, "inspect", name)), &inspection); err != nil {
		t.Fatal(err)
	}
	if inspection.ID != id || inspection.State != container.StateExited || inspection.ExitCode == nil || *inspection.ExitCode != 7 {
		t.Fatalf("completed inspection: %+v", inspection)
	}
	if err := json.Unmarshal([]byte(success(t, "ps", "-a", "--json")), &records); err != nil {
		t.Fatal(err)
	}
	found = false
	for _, record := range records {
		if record.ID == id && record.State == container.StateExited {
			found = true
		}
	}
	if !found {
		t.Fatalf("completed container missing from ps -a: %+v", records)
	}
	if err := json.Unmarshal([]byte(success(t, "ps", "--json")), &records); err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if record.ID == id {
			t.Fatal("completed container remained in active listing")
		}
	}
	if out := success(t, "logs", "--tail", "1", name); out != "wait-ready\n" {
		t.Fatalf("completed logs: %q", out)
	}
	success(t, "rm", name)
	if out, diagnostic, code := command(t, "wait", id); code != 125 || out != "" || !strings.Contains(diagnostic, "container not found") {
		t.Fatalf("removed container wait: exit=%d stdout=%q stderr=%q", code, out, diagnostic)
	}
}

func TestMachineInitPreservesExistingMachine(t *testing.T) {
	success(t, "machine", "start")
	before := success(t, "machine", "status")
	out, diagnostic, code := command(t, "machine", "init", "--cpus", "2", "--memory", "2", "--disk", "10")
	if code != 125 || out != "" || !strings.Contains(diagnostic, "already exists") {
		t.Fatalf("reinitialize: exit=%d stdout=%q stderr=%q", code, out, diagnostic)
	}
	if after := success(t, "machine", "status"); before != after || !strings.Contains(after, "Running") {
		t.Fatalf("existing machine changed: before=%q after=%q", before, after)
	}
}
