//go:build darwin

package macos

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/mingo-liu/casklet/internal/container"
)

func macHealth(t *testing.T, id, status string) container.Inspection {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var i container.Inspection
		if err := json.Unmarshal([]byte(success(t, "inspect", id)), &i); err != nil {
			t.Fatal(err)
		}
		if i.Health != nil && i.Health.Status == status {
			return i
		}
		if time.Now().After(deadline) {
			t.Fatalf("health did not become %s: %+v", status, i.Health)
		}
		time.Sleep(40 * time.Millisecond)
	}
}

func TestHealthStatusThroughMacTransportAndRestart(t *testing.T) {
	id := detached(t, "--health-cmd", `test "$READY_VALUE" = exact && test -f /ready`, "--env", "READY_VALUE=exact", "--health-interval", "100ms", "--health-timeout", "1s", "--health-retries", "2", "--", "/bin/sleep", "300")
	macHealth(t, id, container.HealthUnhealthy)
	success(t, "exec", id, "--", "/bin/touch", "/ready")
	i := macHealth(t, id, container.HealthHealthy)
	if i.State != container.StateRunning || i.Config.Healthcheck.Interval != "100ms" || i.RestartCount != 0 {
		t.Fatal(i)
	}
	if out := success(t, "ps"); !strings.Contains(out, "HEALTH") || !strings.Contains(out, "healthy") {
		t.Fatal(out)
	}
	success(t, "stop", id)
	macHealth(t, id, container.HealthStopped)
	success(t, "start", id)
	i = macHealth(t, id, container.HealthHealthy)
	if i.Generation != 1 {
		t.Fatal(i)
	}
	for _, check := range i.Health.Checks {
		if check.StartedAt.Before(*i.StartedAt) {
			t.Fatal("old health result survived restart")
		}
	}
}

func TestHealthProbeTimeoutThroughMacTransport(t *testing.T) {
	id := detached(t, "--health-cmd", "trap '' TERM; sleep 300 & wait", "--health-interval", "100ms", "--health-timeout", "100ms", "--health-retries", "1", "--", "/bin/sleep", "300")
	i := macHealth(t, id, container.HealthUnhealthy)
	last := i.Health.Checks[len(i.Health.Checks)-1]
	if !last.TimedOut || last.ExitCode != 124 || last.FinishedAt.Sub(last.StartedAt) > time.Second {
		t.Fatal(last)
	}
	success(t, "stop", id)
	macHealth(t, id, container.HealthStopped)
}

func TestOCIHealthcheckThroughMacTransport(t *testing.T) {
	refs := progressRegistry(t)
	imageID := strings.TrimSpace(success(t, "image", "pull", refs[0]))
	t.Cleanup(func() { command(t, "image", "rm", imageID) })
	id := detached(t, "--image", imageID, "--", "/bin/busybox", "sleep", "300")
	i := macHealth(t, id, container.HealthHealthy)
	if i.Config.Healthcheck.Test[0] != "CMD" || i.Config.Healthcheck.Interval != "50ms" {
		t.Fatal(i)
	}
	disabled := detached(t, "--image", imageID, "--no-healthcheck", "--", "/bin/busybox", "sleep", "300")
	var inspection container.Inspection
	if err := json.Unmarshal([]byte(success(t, "inspect", disabled)), &inspection); err != nil {
		t.Fatal(err)
	}
	if inspection.Health != nil || inspection.Config.Healthcheck != nil {
		t.Fatal("inherited healthcheck was not disabled")
	}
}
