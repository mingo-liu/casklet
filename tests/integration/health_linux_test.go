//go:build linux

package integration

import (
	"context"
	"encoding/json"
	"github.com/mingo-liu/casklet/internal/image"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mingo-liu/casklet/internal/container"
)

func healthInspection(t *testing.T, id string) container.Inspection {
	t.Helper()
	var i container.Inspection
	if err := json.Unmarshal([]byte(backgroundSuccess(t, "inspect", id)), &i); err != nil {
		t.Fatal(err)
	}
	return i
}
func awaitHealth(t *testing.T, id, status string) container.Inspection {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		i := healthInspection(t, id)
		if i.Health != nil && i.Health.Status == status {
			return i
		}
		if time.Now().After(deadline) {
			t.Fatalf("health did not become %s: %+v", status, i.Health)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestHealthProbeTransitionsIsolationAndRestart(t *testing.T) {
	require(t)
	probe := `test "$(id -u):$(id -g)" = 1000:1001 && test "$PROBE_VALUE:$PWD:$(hostname)" = expected:/tmp:casklet && test -f /tmp/ready && ! touch /forbidden`
	id := startBackground(t, backgroundName(t), []string{"--user", "1000:1001", "--workdir", "/tmp", "--env", "PROBE_VALUE=expected", "--read-only", "--restart", "always", "--health-cmd", probe, "--health-interval", "50ms", "--health-timeout", "500ms", "--health-retries", "2", "--health-start-period", "300ms", "--health-start-interval", "20ms"}, "/bin/sleep", "300")
	unhealthy := awaitHealth(t, id, container.HealthUnhealthy)
	if unhealthy.State != container.StateRunning || unhealthy.RestartCount != 0 || unhealthy.Health.FailingStreak != 2 {
		t.Fatalf("probe changed lifecycle: %+v", unhealthy)
	}
	backgroundSuccess(t, "exec", id, "--", "/bin/touch", "/tmp/ready")
	healthy := awaitHealth(t, id, container.HealthHealthy)
	if healthy.Health.FailingStreak != 0 {
		t.Fatal(healthy.Health)
	}
	if table := backgroundSuccess(t, "ps"); !strings.Contains(table, "HEALTH") || !strings.Contains(table, "healthy") {
		t.Fatal(table)
	}
	backgroundSuccess(t, "exec", id, "--", "/bin/rm", "/tmp/ready")
	awaitHealth(t, id, container.HealthUnhealthy)
	backgroundSuccess(t, "exec", id, "--", "/bin/touch", "/tmp/ready")
	awaitHealth(t, id, container.HealthHealthy)
	backgroundSuccess(t, "stop", id)
	awaitHealth(t, id, container.HealthStopped)
	backgroundSuccess(t, "start", id)
	awaitHealth(t, id, container.HealthUnhealthy)
	backgroundSuccess(t, "exec", id, "--", "/bin/touch", "/tmp/ready")
	healthy = awaitHealth(t, id, container.HealthHealthy)
	if healthy.Generation != 1 || healthy.Config.Healthcheck.Interval != "50ms" {
		t.Fatalf("restart lost configuration: %+v", healthy)
	}
	for _, check := range healthy.Health.Checks {
		if check.StartedAt.Before(*healthy.StartedAt) {
			t.Fatal("old probe result crossed restart")
		}
	}
}

func TestHealthProbeTimeoutAndStopCleanup(t *testing.T) {
	require(t)
	probe := `trap '' TERM; (trap '' TERM; sleep 300) & echo probe-running; wait`
	id := startBackground(t, backgroundName(t), []string{"--health-cmd", probe, "--health-interval", "20ms", "--health-timeout", "100ms", "--health-retries", "1"}, "/bin/sleep", "300")
	i := awaitHealth(t, id, container.HealthUnhealthy)
	check := i.Health.Checks[len(i.Health.Checks)-1]
	if !check.TimedOut || check.ExitCode != 124 || check.FinishedAt.Sub(check.StartedAt) > time.Second {
		t.Fatalf("probe timeout was not bounded: %+v", check)
	}
	if strings.Contains(backgroundSuccess(t, "logs", id), "probe-running") {
		t.Fatal("probe output entered workload logs")
	}
	record := waitBackground(t, id, "running")
	backgroundSuccess(t, "stop", id)
	awaitHealth(t, id, container.HealthStopped)
	// Runtime cleanup waits for every probe's child cgroup and its descendants.
	assertCgroupRemoved(t, record.Cgroup)
	if _, err := os.Stat(record.RunPath); !os.IsNotExist(err) {
		t.Fatalf("retained probe runtime resources: %v", err)
	}
	assertBackgroundUnitStopped(t, id)
}

func TestOCIImageHealthcheckInheritanceAndDisable(t *testing.T) {
	ref, _ := ociRegistryFixture(t)
	t.Cleanup(func() {
		store, err := image.OpenStore()
		if err != nil {
			t.Error(err)
			return
		}
		record, err := store.Resolve(context.Background(), ref)
		if err != nil {
			t.Error(err)
			return
		}
		if err := store.Remove(context.Background(), record.ID, container.ImageReferenced); err != nil {
			t.Error(err)
		}
	})
	name := backgroundName(t)
	id := backgroundID(t, backgroundSuccess(t, "run", "-d", "--name", name, "--image", ref, "--entrypoint", "", "--", "/bin/sleep", "300"))
	i := awaitHealth(t, id, container.HealthHealthy)
	if i.Config.Healthcheck == nil || i.Config.Healthcheck.Test[0] != "CMD-SHELL" || i.Config.User.UID != 123 {
		t.Fatalf("image health defaults: %+v", i)
	}
	disabled := backgroundName(t)
	disabledID := backgroundID(t, backgroundSuccess(t, "run", "-d", "--name", disabled, "--image", ref, "--no-healthcheck", "--entrypoint", "", "--", "/bin/sleep", "300"))
	if i := healthInspection(t, disabledID); i.Health != nil || i.Config.Healthcheck != nil {
		t.Fatalf("disable ignored: %+v", i)
	}
	overridden := backgroundName(t)
	overriddenID := backgroundID(t, backgroundSuccess(t, "run", "-d", "--name", overridden, "--image", ref, "--health-cmd", "false", "--health-retries", "1", "--health-interval", "20ms", "--entrypoint", "", "--", "/bin/sleep", "300"))
	awaitHealth(t, overriddenID, container.HealthUnhealthy)
}

func TestOCIExecFormHealthcheck(t *testing.T) {
	ref, _ := ociRegistryFixture(t, []string{"CMD", "/bin/sh", "-c", `test "$(id -u):$(id -g)" = 123:456 && test "$IMAGE_VALUE:$PWD" = image:/new-work`})
	t.Cleanup(func() {
		store, err := image.OpenStore()
		if err != nil {
			t.Error(err)
			return
		}
		record, err := store.Resolve(context.Background(), ref)
		if err != nil {
			t.Error(err)
			return
		}
		if err := store.Remove(context.Background(), record.ID, container.ImageReferenced); err != nil {
			t.Error(err)
		}
	})
	name := backgroundName(t)
	id := backgroundID(t, backgroundSuccess(t, "run", "-d", "--name", name, "--image", ref, "--entrypoint", "", "--", "/bin/sleep", "300"))
	i := awaitHealth(t, id, container.HealthHealthy)
	if i.Config.Healthcheck.Test[0] != "CMD" {
		t.Fatal(i)
	}
}
