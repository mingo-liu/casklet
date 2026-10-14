package machine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func resizeMachine(t *testing.T, record any, failEdit bool) (*Machine, string) {
	t.Helper()
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if record == nil {
		data = nil
	}
	directory := t.TempDir()
	log := filepath.Join(directory, "calls")
	stub := filepath.Join(directory, "limactl")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> " + quote(log) + "\nif [ \"$2\" = list ]; then printf '%s' " + quote(string(data)) + "; fi\n"
	if failEdit {
		script += "if [ \"$2\" = edit ]; then echo 'edit rejected' >&2; exit 1; fi\n"
	}
	if err := os.WriteFile(stub, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return &Machine{lima: stub, directory: directory, stderr: io.Discard}, log
}

func resizeRecord() map[string]any {
	return map[string]any{
		"name": Name, "status": "Stopped", "vmType": "vz", "arch": map[string]string{"arm64": "aarch64", "amd64": "x86_64"}[runtime.GOARCH],
		"cpus": 4, "memory": 4 * gib, "disk": 20 * gib, "config": map[string]any{"os": "Linux", "plain": false},
	}
}

func TestParseResizeRequiresBoundedExplicitResources(t *testing.T) {
	for _, args := range [][]string{
		{}, {"--cpus", "0"}, {"--memory", "0"}, {"--disk", "0"}, {"--cpus", "-1"}, {"--cpus", "65"}, {"--memory", "129"},
		{"--disk", "3"}, {"--disk", "1025"}, {"--memory", "1.5"}, {"--disk"}, {"--unknown", "1"}, {"--mount", "/tmp"}, {"--cpus", "2", "extra"},
	} {
		if _, err := parseHostCommand(append([]string{"machine", "resize"}, args...)); err == nil {
			t.Fatalf("accepted %q", args)
		}
	}
	for _, tc := range []struct {
		args []string
		want Options
	}{
		{[]string{"--cpus=1"}, Options{CPUs: 1}},
		{[]string{"--memory", "128"}, Options{Memory: 128}},
		{[]string{"--disk", "1024"}, Options{Disk: 1024}},
		{[]string{"--cpus", "64", "--memory", "1", "--disk", "4"}, Options{CPUs: 64, Memory: 1, Disk: 4}},
	} {
		options, err := parseHostCommand(append([]string{"machine", "resize"}, tc.args...))
		if err != nil || options.CPUs != tc.want.CPUs || options.Memory != tc.want.Memory || options.Disk != tc.want.Disk {
			t.Fatalf("%q: %+v %v", tc.args, options, err)
		}
	}
}

func TestResizeEditsOnlyRequestedChangedResources(t *testing.T) {
	for _, tc := range []struct {
		name    string
		options Options
		flags   string
	}{
		{"all", Options{CPUs: 6, Memory: 8, Disk: 40}, "--cpus\n6\n--memory\n8\n--disk\n40\n"},
		{"cpu reduction", Options{CPUs: 2}, "--cpus\n2\n"},
		{"memory reduction", Options{Memory: 2}, "--memory\n2\n"},
		{"disk only", Options{Disk: 40}, "--disk\n40\n"},
		{"unchanged omitted", Options{CPUs: 4, Memory: 8, Disk: 20}, "--memory\n8\n"},
		{"idempotent", Options{CPUs: 4, Memory: 4, Disk: 20}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, log := resizeMachine(t, resizeRecord(), false)
			// Sentinel payloads stand for existing VM/engine data. The editor must
			// receive only resource flags, never replacement templates or storage commands.
			sentinels := []string{"disk", "containers", "images", "volumes", "machine.yaml"}
			for _, name := range sentinels {
				if err := os.WriteFile(filepath.Join(m.directory, name), []byte(name), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var out bytes.Buffer
			if err := m.resize(context.Background(), tc.options, &out); err != nil {
				t.Fatal(err)
			}
			calls, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			want := "--tty=false\nlist\n--json\n"
			if tc.flags != "" {
				want += "--tty=false\nedit\n" + tc.flags + Name + "\n"
			}
			if string(calls) != want {
				t.Fatalf("calls:\n%s\nwant:\n%s", calls, want)
			}
			for _, name := range sentinels {
				got, err := os.ReadFile(filepath.Join(m.directory, name))
				if err != nil || string(got) != name {
					t.Fatalf("changed %s: %s %v", name, got, err)
				}
			}
			if tc.flags != "" && !strings.Contains(out.String(), "next start") {
				t.Fatalf("missing apply instructions: %s", &out)
			}
		})
	}
}

func TestResizeRejectsMissingRunningAndUnsafeMachinesBeforeEdit(t *testing.T) {
	for _, scenario := range []string{"missing", "other machine", "malformed", "Running", "Starting", "Broken", "plain", "wrong arch", "wrong VM", "wrong OS", "missing resources", "shrink", "fractional shrink"} {
		t.Run(scenario, func(t *testing.T) {
			record := resizeRecord()
			var input any = record
			options := Options{CPUs: 6}
			switch scenario {
			case "missing":
				input = nil
			case "other machine":
				record["name"] = "other-machine"
			case "malformed":
				input = "invalid instance"
			case "Running", "Starting", "Broken":
				record["status"] = scenario
			case "plain":
				record["config"].(map[string]any)["plain"] = true
			case "wrong arch":
				record["arch"] = "other"
			case "wrong VM":
				record["vmType"] = "qemu"
			case "wrong OS":
				record["config"].(map[string]any)["os"] = "Darwin"
			case "missing resources":
				record["disk"] = 0
			case "shrink":
				options.Disk = 19
			case "fractional shrink":
				record["disk"] = 20*gib + 1
				options.Disk = 20
			}
			m, log := resizeMachine(t, input, false)
			var out bytes.Buffer
			if err := m.resize(context.Background(), options, &out); err == nil {
				t.Fatal("accepted unsafe resize")
			}
			calls, _ := os.ReadFile(log)
			if string(calls) != "--tty=false\nlist\n--json\n" || out.Len() != 0 {
				t.Fatalf("mutated VM: %s %s", calls, &out)
			}
		})
	}
}

func TestResizeReportsEditFailureWithoutSuccess(t *testing.T) {
	m, log := resizeMachine(t, resizeRecord(), true)
	var out bytes.Buffer
	err := m.resize(context.Background(), Options{Disk: 40}, &out)
	if err == nil || !strings.Contains(err.Error(), "edit rejected") || out.Len() != 0 {
		t.Fatalf("error/output: %v %s", err, &out)
	}
	calls, _ := os.ReadFile(log)
	if strings.Contains(string(calls), "\nstart\n") || strings.Contains(string(calls), "\nstop\n") {
		t.Fatalf("changed state: %s", calls)
	}
}

func TestResizeLockHonorsCancellation(t *testing.T) {
	m, log := resizeMachine(t, resizeRecord(), false)
	lock, err := m.lock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := m.resize(ctx, Options{CPUs: 2}, io.Discard); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lock wait: %v", err)
	}
	if _, err := os.Stat(log); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("called Lima while locked: %v", err)
	}
}

func TestStatusReportsConfiguredResources(t *testing.T) {
	var out bytes.Buffer
	_, err := writeStatus(&out, Instance{Name: Name, Status: "Stopped", Arch: "aarch64", CPUs: 6, Memory: 8 * gib, Disk: 40 * gib})
	if err != nil || out.String() != Name+"\tStopped\taarch64\tcpus=6\tmemory=8GiB\tdisk=40GiB (configured)\n" {
		t.Fatalf("status: %s %v", &out, err)
	}
}

func TestRequireRunningPreviewNeverStartsOrCreatesMachine(t *testing.T) {
	for _, record := range []any{nil, resizeRecord()} {
		machine, log := resizeMachine(t, record, false)
		if _, err := machine.ensure(context.Background(), defaultOptions(), false, true); err == nil || !strings.Contains(err.Error(), "machine start") {
			t.Fatalf("preview error: %v", err)
		}
		data, err := os.ReadFile(log)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "start") || strings.Contains(string(data), "shell") || strings.Contains(string(data), "copy") {
			t.Fatalf("preview mutated machine: %s", data)
		}
	}
}
