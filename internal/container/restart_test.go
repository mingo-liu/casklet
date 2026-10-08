package container

import (
	"testing"
	"time"

	"github.com/mingo-liu/casklet/internal/config"
)

func TestRestartDecisionHonorsStopBootSuccessAndLimit(t *testing.T) {
	exit0, exit1 := 0, 1
	for _, tc := range []struct {
		name, policy   string
		exit           *int
		manual         bool
		oldBoot        bool
		stoppedCurrent bool
		count          uint64
		want           bool
	}{
		{name: "legacy no", exit: &exit1}, {name: "success", policy: "on-failure", exit: &exit0}, {name: "failure", policy: "on-failure", exit: &exit1, want: true}, {name: "lost supervisor", policy: "on-failure", want: true},
		{name: "cap", policy: "on-failure:2", exit: &exit1, count: 2}, {name: "next retry", policy: "on-failure:2", exit: &exit1, count: 1, want: true},
		{name: "manual always", policy: "always", exit: &exit1, manual: true}, {name: "manual unless", policy: "unless-stopped", exit: &exit1, manual: true},
		{name: "boot always", policy: "always", manual: true, oldBoot: true, want: true}, {name: "stop during boot", policy: "always", manual: true, oldBoot: true, stoppedCurrent: true},
		{name: "boot unless stopped", policy: "unless-stopped", manual: true, oldBoot: true}, {name: "boot unless running", policy: "unless-stopped", oldBoot: true, want: true}, {name: "boot failure policy", policy: "on-failure", oldBoot: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := Record{State: StateExited, ExitCode: tc.exit, StoppedByUser: tc.manual, RestartCount: tc.count, BootID: "current"}
			if tc.oldBoot {
				r.BootID = "old"
				r.StoppedBootID = "old"
			}
			if tc.stoppedCurrent {
				r.StoppedBootID = "current"
			}
			got, _ := restartDecision(r, config.Config{RestartPolicy: tc.policy}, "current")
			if got != tc.want {
				t.Fatalf("eligible %v want %v", got, tc.want)
			}
		})
	}
}
func TestRestartBackoffAndHealthyReset(t *testing.T) {
	for count, want := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second} {
		if got := restartDelay(uint64(count)); got != want {
			t.Fatalf("delay %d: %s", count, got)
		}
	}
	start := time.Now().Add(-11 * time.Second)
	finish := time.Now()
	exit := 1
	r := Record{State: StateExited, ExitCode: &exit, StartedAt: &start, FinishedAt: &finish, RestartCount: 10}
	eligible, count := restartDecision(r, config.Config{RestartPolicy: "on-failure:2"}, "boot")
	if !eligible || count != 0 {
		t.Fatal("healthy execution did not reset retry cap")
	}
	r.RestartAt = &finish
	eligible, _ = restartDecision(r, config.Config{RestartPolicy: "on-failure:2"}, "boot")
	if eligible {
		t.Fatal("rescheduled failure reset retry cap again")
	}
	r.BootID = "old"
	r.RestartBootID = "old"
	_, count = restartDecision(r, config.Config{RestartPolicy: "always"}, "boot")
	if count != 0 {
		t.Fatal("pending retry did not reset across boots")
	}
	r.RestartBootID = "boot"
	_, count = restartDecision(r, config.Config{RestartPolicy: "always"}, "boot")
	if count != 10 {
		t.Fatal("new-boot preparation failures reset repeatedly")
	}
}
