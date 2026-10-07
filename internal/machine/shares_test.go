package machine

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSharePreservesMachineAndRejectsUnsafeChanges(t *testing.T) {
	for _, state := range []string{"Stopped", "Running", "missing", "already shared", "overlap", "read only"} {
		t.Run(state, func(t *testing.T) {
			directory, err := sharedDirectory(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			record := map[string]any{"name": Name, "status": "Stopped", "config": map[string]any{"mounts": []any{}}}
			cfg := record["config"].(map[string]any)
			if state == "Running" {
				record["status"] = "Running"
			}
			if state == "already shared" || state == "read only" {
				cfg["mounts"] = []any{map[string]any{"location": directory, "writable": state != "read only"}}
			}
			if state == "overlap" {
				cfg["mounts"] = []any{map[string]any{"location": filepath.Join(directory, "child"), "writable": true}}
			}
			data, _ := json.Marshal(record)
			if state == "missing" {
				data = nil
			}
			log := filepath.Join(t.TempDir(), "calls")
			stub := filepath.Join(t.TempDir(), "limactl")
			script := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> " + quote(log) + "\nif [ \"$2\" = list ]; then printf '%s\\n' " + quote(string(data)) + "; fi\n"
			if state == "missing" {
				script = "#!/bin/sh\nprintf '%s\\n' \"$@\" >> " + quote(log) + "\n"
			}
			if err := os.WriteFile(stub, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			m := &Machine{lima: stub, directory: t.TempDir(), stderr: io.Discard}
			var out bytes.Buffer
			err = m.share(context.Background(), directory, &out)
			calls, _ := os.ReadFile(log)
			edited := strings.Contains(string(calls), "\nedit\n")
			if state == "Stopped" {
				if err != nil || !edited || !strings.Contains(out.String(), "machine start") {
					t.Fatalf("share: %s %s %v", calls, &out, err)
				}
				if !strings.Contains(string(calls), `"writable":true`) {
					t.Fatal("share is not writable")
				}
			} else if state == "already shared" {
				if err != nil || edited {
					t.Fatalf("repeat share: %s %v", calls, err)
				}
			} else if err == nil || edited {
				t.Fatalf("unsafe share: %s %v", calls, err)
			}
			if strings.Contains(string(calls), "\nstop\n") || strings.Contains(string(calls), "\nstart\n") {
				t.Fatal("changed machine state implicitly")
			}
		})
	}
}

func TestParseShareValidatesDirectoryBeforeLima(t *testing.T) {
	for _, args := range [][]string{{"machine", "share"}, {"machine", "share", ""}, {"machine", "share", "/"}, {"machine", "share", filepath.Join(t.TempDir(), "missing")}} {
		if _, err := parseHostCommand(args); err == nil {
			t.Fatalf("accepted %q", args)
		}
	}
}
