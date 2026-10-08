//go:build darwin

package macos

import (
	"strings"
	"testing"
	"time"
)

func TestManagedLinuxStopSignalThroughMacClient(t *testing.T) {
	id := detached(t, "--stop-signal", "SIGUSR1", "--stop-timeout", "2s", "--", "/bin/sh", "-c", "trap 'echo custom-stop; exit 23' USR1; echo signal-ready; while :; do sleep .05; done")
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(success(t, "logs", id), "signal-ready") {
		if time.Now().After(deadline) {
			t.Fatal("not ready")
		}
		time.Sleep(25 * time.Millisecond)
	}
	success(t, "stop", id)
	out, stderr, code := command(t, "wait", id)
	if code != 23 || out != "23\n" || stderr != "" {
		t.Fatalf("wait %d %q %q", code, out, stderr)
	}
	if out := success(t, "inspect", id); !strings.Contains(out, `"stop_signal": "SIGUSR1"`) {
		t.Fatal(out)
	}
}
