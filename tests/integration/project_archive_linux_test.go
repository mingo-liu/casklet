//go:build linux

package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mingo-liu/casklet/internal/container"
	"github.com/mingo-liu/casklet/internal/project"
)

func TestLoadedOfflineImageProjectPreviewAndSelectedRecreation(t *testing.T) {
	require(t)
	t.Setenv("CASKLET_MACHINE_STATE_DIR", t.TempDir())
	reference, closeRegistry := ociRegistryFixture(t)
	id := strings.TrimSpace(backgroundSuccess(t, "image", "pull", reference))
	closeRegistry()
	archive := []byte(backgroundSuccess(t, "image", "save", id))
	backgroundSuccess(t, "image", "rm", id)
	cmd := exec.Command(binary, "image", "load")
	cmd.Stdin = bytes.NewReader(archive)
	var output, diagnostic bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &diagnostic
	if err := cmd.Run(); err != nil || output.String() != id+"\n" {
		t.Fatalf("offline load %v %q %q", err, output.String(), diagnostic.String())
	}
	name := fmt.Sprintf("offline-project-%x", time.Now().UnixNano())
	t.Cleanup(func() { backgroundCLI(t, "project", "down", "--volumes", name); backgroundCLI(t, "image", "rm", id) })
	imageRun := map[string]any{"image": id, "network": "none", "command": []string{"sleep 300"}, "mount": []string{"type=volume,source=data,target=/data,readonly"}}
	document := map[string]any{"name": name, "networks": []string{}, "volumes": []string{"data"}, "services": map[string]any{
		"app":    map[string]any{"run": imageRun},
		"writer": map[string]any{"run": map[string]any{"rootfs": template, "network": "none", "command": []string{"/bin/sleep", "300"}, "mount": []string{"type=volume,source=data,target=/data"}}},
	}}
	file := filepath.Join(t.TempDir(), "project.json")
	save := func() {
		t.Helper()
		data, err := json.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	save()
	backgroundSuccess(t, "project", "up", "--config", file, "--wait", "--timeout", "30s")
	inspect := func(key string) container.Inspection {
		t.Helper()
		var value container.Inspection
		out := backgroundSuccess(t, "inspect", "cp-"+name+"-"+key)
		if err := json.Unmarshal([]byte(out), &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	old, writer := inspect("app"), inspect("writer")
	if old.Config.Image != id || !old.Config.OCI || old.Config.Healthcheck == nil || old.Health == nil || old.Health.Status != container.HealthHealthy {
		t.Fatalf("loaded image defaults not inherited: %+v", old)
	}
	if old.Config.User.UID != 123 || old.Config.User.GID != 456 || old.Config.Workdir != "/new-work" || old.Config.StopSignal != "SIGUSR1" || len(old.Config.Command) != 3 || old.Config.Command[0] != "/bin/sh" {
		t.Fatalf("loaded launch defaults %+v", old.Config)
	}
	backgroundSuccess(t, "exec", writer.ID, "--", "/bin/sh", "-c", "printf archived-project-data > /data/message")
	imageRun["env"] = []string{"UPGRADE=selected"}
	save()
	before := backgroundSuccess(t, "project", "ps", "--json", name)
	var changes []project.Change
	previewOutput := backgroundSuccess(t, "project", "up", "--config", file, "--recreate", "app", "--dry-run", "--json")
	if json.Unmarshal([]byte(previewOutput), &changes) != nil || len(changes) != 2 || changes[0].Action != "recreate" || changes[0].ID != old.ID || changes[1].Action != "reuse" || changes[1].ID != writer.ID {
		t.Fatalf("preview actions %s", previewOutput)
	}
	if after := backgroundSuccess(t, "project", "ps", "--json", name); after != before {
		t.Fatalf("offline preview changed project: %s", after)
	}
	backgroundSuccess(t, "project", "up", "--config", file, "--recreate", "app", "--wait", "--timeout", "30s")
	replaced, unchanged := inspect("app"), inspect("writer")
	if replaced.ID == old.ID || replaced.Config.Image != id || unchanged.ID != writer.ID || unchanged.Generation != writer.Generation || unchanged.State != container.StateRunning || replaced.Health == nil || replaced.Health.Status != container.HealthHealthy {
		t.Fatalf("offline upgrade: app=%+v writer=%+v", replaced, unchanged)
	}
	if got := backgroundSuccess(t, "exec", replaced.ID, "--", "/bin/sh", "-c", `printf '%s:%s:%s:' "$IMAGE_VALUE" "$PWD" "$UPGRADE"; cat /data/message`); got != "image:/new-work:selected:archived-project-data" {
		t.Fatalf("loaded default/persistent state %q", got)
	}
	backgroundSuccess(t, "project", "down", name)
	backgroundSuccess(t, "volume", "inspect", "cp-"+name+"-data")
}
