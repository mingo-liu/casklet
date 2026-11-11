//go:build linux

package integration

import (
	"testing"
)

func TestStructuredExitReasonsAndRestartHistory(t *testing.T) {
	for _, tc := range []struct {
		name, reason     string
		options, command []string
		code             int
	}{
		{name: "exit", reason: "exited", options: nil, command: []string{"/bin/sh", "-c", "exit 7"}, code: 7},
		{name: "literal-124", reason: "exited", options: nil, command: []string{"/bin/sh", "-c", "exit 124"}, code: 124},
		{name: "literal-137", reason: "exited", options: nil, command: []string{"/bin/sh", "-c", "exit 137"}, code: 137},
		{name: "timeout", reason: "timeout", options: []string{"--timeout", "200ms", "--stop-timeout", "0s"}, command: []string{"/bin/sleep", "30"}, code: 124},
		{name: "oom", reason: "oom_killed", options: []string{"--memory", "32m", "--timeout", "15s"}, command: []string{"/bin/integration-helper", "memory"}, code: 137},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := startBackground(t, backgroundName(t), tc.options, tc.command...)
			assertBackgroundExit(t, waitBackground(t, id, "exited"), tc.code)
			got := inspectBackground(t, id)
			if got.ExitReason != tc.reason || got.OOMKilled != (tc.reason == "oom_killed") {
				t.Fatalf("exit result: %+v", got)
			}
			backgroundSuccess(t, "start", id)
			// A fast workload may already have finished its new execution.
			got = inspectBackground(t, id)
			if got.Generation != 1 || got.PreviousExit == nil || got.PreviousExit.ExitReason != tc.reason || got.PreviousExit.OOMKilled != (tc.reason == "oom_killed") {
				t.Fatalf("missing previous result: %+v", got)
			}
		})
	}
	t.Run("stop", func(t *testing.T) {
		id := startBackground(t, backgroundName(t), nil, "/bin/sleep", "30")
		if got := inspectBackground(t, id); got.ExitReason != "" || got.OOMKilled {
			t.Fatalf("active result: %+v", got)
		}
		backgroundSuccess(t, "stop", id)
		if got := inspectBackground(t, id); got.ExitReason != "stopped" || got.OOMKilled {
			t.Fatalf("stop result: %+v", got)
		}
	})
	t.Run("startup-failure", func(t *testing.T) {
		name := backgroundName(t)
		code, _, _ := backgroundCLI(t, detachedArguments(name, nil, "/missing-command")...)
		if code != 125 {
			t.Fatalf("startup exit=%d", code)
		}
		got := inspectBackground(t, name)
		if got.ExitReason != "startup_failed" || got.OOMKilled {
			t.Fatalf("startup result: %+v", got)
		}
	})
}
