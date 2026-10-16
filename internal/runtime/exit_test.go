package runtime

import "testing"

func TestExitStatusKeepsObservedCausesSeparate(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  ExitStatus
		started bool
		want    string
	}{
		{"startup", ExitStatus{}, false, "startup_failed"},
		{"runtime", ExitStatus{}, true, "runtime_error"},
		{"command", ExitStatus{ExitReason: "exited"}, true, "exited"},
		{"timeout", ExitStatus{ExitReason: "timeout"}, true, "timeout"},
		{"stop", ExitStatus{ExitReason: "stopped"}, true, "stopped"},
		{"oom-after-timeout", ExitStatus{ExitReason: "timeout", OOMKilled: true}, true, "oom_killed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status := tc.status
			status.finish(tc.started)
			if status.ExitReason != tc.want || status.OOMKilled != tc.status.OOMKilled {
				t.Fatalf("result=%+v", status)
			}
		})
	}
}
