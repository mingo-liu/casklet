//go:build linux

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/mingo-liu/casklet/internal/config"
	"github.com/mingo-liu/casklet/internal/container"
)

func startRestartManager(t *testing.T) {
	t.Helper()
	unit := "casklet-restarts.service"
	if output, err := exec.Command("systemd-run", "--quiet", "--service-type=exec", "--unit="+unit, "--", binary, "__restart-manager").CombinedOutput(); err != nil {
		t.Fatalf("manager: %v %s", err, output)
	}
	t.Cleanup(func() {
		exec.Command("systemctl", "stop", unit).Run()
		exec.Command("systemctl", "reset-failed", unit).Run()
	})
}
func restartInspection(t *testing.T, id string) container.Inspection {
	t.Helper()
	var r container.Inspection
	if err := json.Unmarshal([]byte(backgroundSuccess(t, "inspect", id)), &r); err != nil {
		t.Fatal(err)
	}
	return r
}
func awaitRestart(t *testing.T, id string, check func(container.Inspection) bool) container.Inspection {
	t.Helper()
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		r := restartInspection(t, id)
		if check(r) {
			return r
		}
		time.Sleep(40 * time.Millisecond)
	}
	t.Fatalf("restart state: %+v", restartInspection(t, id))
	return container.Inspection{}
}
func TestAutomaticRestartPoliciesAndManualStop(t *testing.T) {
	require(t)
	startRestartManager(t)
	t.Run("limited failures preserve data and receipts", func(t *testing.T) {
		id := startBackground(t, backgroundName(t), []string{"--restart", "on-failure:2"}, "/bin/sh", "-c", "n=0; [ ! -f /counter ] || n=$(cat /counter); n=$((n+1)); echo $n > /counter; echo attempt-$n; exit 7")
		r := awaitRestart(t, id, func(r container.Inspection) bool { return r.Generation == 2 && r.ExitCode != nil })
		if r.RestartCount != 2 || r.PreviousExit == nil || *r.PreviousExit.ExitCode != 7 {
			t.Fatalf("retry state %+v", r)
		}
		if got := strings.Count(backgroundSuccess(t, "logs", id), "attempt-"); got != 3 {
			t.Fatalf("attempts %d", got)
		}
		time.Sleep(1200 * time.Millisecond)
		if r := restartInspection(t, id); r.Generation != 2 {
			t.Fatal("retry cap exceeded")
		}
		store, err := container.OpenStore()
		if err != nil {
			t.Fatal(err)
		}
		receipt, done, err := store.Completion(context.Background(), id, 0)
		if err != nil || !done || receipt.ExitCode == nil || *receipt.ExitCode != 7 {
			t.Fatalf("old receipt %+v %v", receipt, err)
		}
		backgroundSuccess(t, "start", id)
		awaitRestart(t, id, func(r container.Inspection) bool { return r.Generation >= 4 })
		backgroundSuccess(t, "stop", id)
	})
	t.Run("successful exit restarts and manual stop persists", func(t *testing.T) {
		id := startBackground(t, backgroundName(t), []string{"--restart", "always"}, "/bin/sh", "-c", "n=0; [ ! -f /counter ] || n=$(cat /counter); n=$((n+1)); echo $n > /counter; echo attempt-$n; [ $n -eq 1 ] && exit 0; exec sleep 300")
		awaitRestart(t, id, func(r container.Inspection) bool { return r.Generation == 1 && r.State == container.StateRunning })
		if out := backgroundSuccess(t, "exec", id, "--", "/bin/cat", "/counter"); out != "2\n" {
			t.Fatal(out)
		}
		backgroundSuccess(t, "stop", id)
		r := restartInspection(t, id)
		time.Sleep(1400 * time.Millisecond)
		after := restartInspection(t, id)
		if !after.StoppedByUser || after.Generation != r.Generation || after.State == container.StateRunning {
			t.Fatalf("manual stop undone %+v", after)
		}
	})
	t.Run("stop and removal during backoff", func(t *testing.T) {
		id := startBackground(t, backgroundName(t), []string{"--restart", "always"}, "/bin/sh", "-c", "exit 0")
		awaitRestart(t, id, func(r container.Inspection) bool { return r.RestartAt != nil })
		backgroundSuccess(t, "stop", id)
		generation := restartInspection(t, id).Generation
		time.Sleep(1200 * time.Millisecond)
		if r := restartInspection(t, id); r.Generation != generation || !r.StoppedByUser {
			t.Fatalf("backoff stop %+v", r)
		}
		backgroundSuccess(t, "rm", id)
	})
	t.Run("more than four services do not starve", func(t *testing.T) {
		ids := []string{}
		for i := 0; i < 6; i++ {
			ids = append(ids, startBackground(t, backgroundName(t), []string{"--restart", "unless-stopped"}, "/bin/sh", "-c", "[ -f /resumed ] || { touch /resumed; exit 1; }; exec sleep 300"))
		}
		for _, id := range ids {
			awaitRestart(t, id, func(r container.Inspection) bool { return r.Generation == 1 && r.State == container.StateRunning })
			backgroundSuccess(t, "stop", id)
		}
	})
}
func TestAutomaticRestartRestoresPreviousBootPolicies(t *testing.T) {
	require(t)
	store, err := container.OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, policy := range []string{"always", "unless-stopped", "on-failure"} {
		id := startBackground(t, backgroundName(t), []string{"--restart", policy}, "/bin/sleep", "300")
		backgroundSuccess(t, "stop", id)
		ids[policy] = id
		if err := store.Update(context.Background(), id, func(r *container.Record) error {
			r.BootID = "00000000-0000-0000-0000-000000000001"
			r.StoppedBootID = r.BootID
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	// A second unless-stopped container was running at shutdown, rather than manually stopped.
	id := startBackground(t, backgroundName(t), []string{"--restart", "unless-stopped"}, "/bin/sleep", "300")
	backgroundSuccess(t, "stop", id)
	if err := store.Update(context.Background(), id, func(r *container.Record) error {
		r.BootID = "00000000-0000-0000-0000-000000000001"
		r.StoppedByUser = false
		r.StoppedBootID = ""
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ids["running-unless"] = id
	startRestartManager(t)
	for _, key := range []string{"always", "running-unless"} {
		awaitRestart(t, ids[key], func(r container.Inspection) bool { return r.Generation == 1 && r.State == container.StateRunning })
		backgroundSuccess(t, "stop", ids[key])
	}
	for _, key := range []string{"unless-stopped", "on-failure"} {
		if r := restartInspection(t, ids[key]); r.Generation != 0 || r.State == container.StateRunning {
			t.Fatalf("unexpected boot restore for %s: %+v", key, r)
		}
	}
	// Preparation failures consume bounded retries without publishing a generation.
	cfg := config.Config{RootFS: "/nonexistent-casklet-restart-template", Command: []string{"sh"}, RestartPolicy: "on-failure:1"}
	record, err := store.Create(context.Background(), cfg, fmt.Sprintf("restart-invalid-%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(context.Background(), record.ID, 0, func(r *container.Record) { r.State = container.StateFailed; now := time.Now(); r.FinishedAt = &now }); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { backgroundCLI(t, "stop", record.ID); backgroundCLI(t, "rm", record.ID) })
	awaitRestart(t, record.ID, func(r container.Inspection) bool { return r.RestartCount == 1 })
	time.Sleep(1300 * time.Millisecond)
	if r := restartInspection(t, record.ID); r.RestartCount != 1 || r.Generation != 0 {
		t.Fatalf("unbounded preparation retry %+v", r)
	}
}
