package runtime

import "testing"

func TestExitStatusKeepsObservedCausesSeparate(t *testing.T) {
	tests := []struct {
		name    string
		status  ExitStatus
		started bool
		want    string
	}{
		{name: "startup", status: ExitStatus{}, started: false, want: "startup_failed"},
		{name: "runtime", status: ExitStatus{}, started: true, want: "runtime_error"},
		{name: "command", status: ExitStatus{ExitReason: "exited"}, started: true, want: "exited"},
		{name: "timeout", status: ExitStatus{ExitReason: "timeout"}, started: true, want: "timeout"},
		{name: "stop", status: ExitStatus{ExitReason: "stopped"}, started: true, want: "stopped"},
		{name: "oom-after-timeout", status: ExitStatus{ExitReason: "timeout", OOMKilled: true}, started: true, want: "oom_killed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status := tt.status
			status.finish(tt.started)
			if status.ExitReason != tt.want || status.OOMKilled != tt.status.OOMKilled {
				t.Fatalf("result=%+v", status)
			}
		})
	}
}
