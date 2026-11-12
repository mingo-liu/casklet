//go:build darwin

package macos

import (
	"encoding/json"
	"testing"

	"github.com/mingo-liu/casklet/internal/container"
)

func TestStructuredExitReasonsThroughMacClient(t *testing.T) {
	inspect := func(id string) container.Inspection {
		var result container.Inspection
		if err := json.Unmarshal([]byte(success(t, "inspect", id)), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	tests := []struct {
		name, reason string
		args         []string
		code         int
	}{
		{name: "literal-137", reason: "exited", args: []string{"--", "/bin/sh", "-c", "exit 137"}, code: 137},
		{name: "timeout", reason: "timeout", args: []string{"--timeout", "200ms", "--stop-timeout", "0s", "--", "/bin/sleep", "30"}, code: 124},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := detached(t, tt.args...)
			_, _, code := command(t, "wait", id)
			if code != tt.code {
				t.Fatalf("wait exit=%d", code)
			}
			result := inspect(id)
			if result.ExitReason != tt.reason || result.OOMKilled {
				t.Fatalf("exit result: %+v", result)
			}
			success(t, "start", id)
			result = inspect(id)
			if result.Generation != 1 || result.PreviousExit == nil || result.PreviousExit.ExitReason != tt.reason || result.PreviousExit.OOMKilled {
				t.Fatalf("restart history: %+v", result)
			}
		})
	}
	t.Run("stop", func(t *testing.T) {
		id := detached(t, "--", "/bin/sleep", "30")
		if result := inspect(id); result.ExitReason != "" || result.OOMKilled {
			t.Fatalf("active exit result: %+v", result)
		}
		success(t, "stop", id)
		if result := inspect(id); result.ExitReason != "stopped" || result.OOMKilled {
			t.Fatalf("stop result: %+v", result)
		}
	})
}
