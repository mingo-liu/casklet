//go:build darwin

package macos

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/mingo-liu/casklet/internal/container"
)

func TestAutomaticRestartThroughMacClient(t *testing.T) {
	id := detached(t, "--restart", "on-failure:2", "--", "/bin/sh", "-c", "echo retained >> /count; [ $(wc -l < /count) -eq 1 ] && exit 7; exec sleep 300")
	waitForContents(t, id, "retained\nretained\n")
	inspect := func() container.Inspection {
		var r container.Inspection
		if err := json.Unmarshal([]byte(success(t, "inspect", id)), &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	r := inspect()
	if r.Generation != 1 || r.RestartCount != 1 || r.Config.RestartPolicy != "on-failure:2" || r.PreviousExit == nil || r.PreviousExit.ExitCode == nil || *r.PreviousExit.ExitCode != 7 {
		t.Fatalf("restart inspection: %+v", r)
	}
	success(t, "stop", "--timeout", "0s", id)
	time.Sleep(1500 * time.Millisecond)
	if after := inspect(); !after.StoppedByUser || after.Generation != r.Generation || after.RestartAt != nil {
		t.Fatalf("manual stop: %+v", after)
	}
	success(t, "start", id)
	waitForContents(t, id, "retained\nretained\nretained\n")
	if r := inspect(); r.StoppedByUser || r.RestartCount != 0 {
		t.Fatalf("manual start did not reset policy state: %+v", r)
	}
}
