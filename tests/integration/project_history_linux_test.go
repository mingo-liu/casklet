//go:build linux

package integration

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	namepkg "github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/mingo-liu/casklet/internal/project"
)

func TestProjectHistoryRollbackPinsImagesAndRecoversFailedReplacement(t *testing.T) {
	require(t)
	t.Setenv("CASKLET_MACHINE_STATE_DIR", t.TempDir())
	reference, closeRegistry := ociRegistryFixture(t)
	defer closeRegistry()
	name := fmt.Sprintf("history-%x", time.Now().UnixNano())
	dir := t.TempDir()
	file := filepath.Join(dir, "project.json")
	envfile := filepath.Join(dir, "app.env")
	run := map[string]any{"image": reference, "entrypoint": "", "user": "0", "network": "none", "no-healthcheck": true, "env-file": []string{"app.env"}, "mount": []string{"type=volume,source=data,target=/data"}, "command": []string{"/bin/sleep", "300"}}
	doc := map[string]any{"name": name, "networks": []string{}, "volumes": []string{"data"}, "services": map[string]any{"app": map[string]any{"run": run}}}
	save := func(version string) {
		t.Helper()
		os.WriteFile(envfile, []byte("APP_VERSION="+version+"\n"), 0600)
		data, _ := json.Marshal(doc)
		if err := os.WriteFile(file, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	history := func() []project.DeploymentSummary {
		t.Helper()
		var result []project.DeploymentSummary
		data := backgroundSuccess(t, "project", "history", "--json", name)
		if strings.Contains(data, "APP_VERSION") || json.Unmarshal([]byte(data), &result) != nil {
			t.Fatalf("public history: %s", data)
		}
		return result
	}
	t.Cleanup(func() { backgroundCLI(t, "project", "down", "--volumes", name) })
	save("one")
	backgroundSuccess(t, "project", "up", "--config", file, "--wait")
	ref := "cp-" + name + "-app"
	old := inspectBackground(t, ref)
	backgroundSuccess(t, "exec", ref, "--", "/bin/sh", "-c", "echo persistent > /data/value")
	backgroundSuccess(t, "project", "up", "--config", file)
	if len(history()) != 1 || history()[0].Images["app"] != old.Config.Image {
		t.Fatal("initial pin/no-op history")
	}
	parsed, _ := namepkg.ParseReference(reference)
	img, err := remote.Image(parsed)
	if err != nil {
		t.Fatal(err)
	}
	cf, err := img.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	cf.Config.Env = append(cf.Config.Env, "IMAGE_VALUE=new")
	updated, err := mutate.ConfigFile(img, cf)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(parsed, updated); err != nil {
		t.Fatal(err)
	}
	freshID := strings.TrimSpace(backgroundSuccess(t, "image", "pull", reference))
	t.Cleanup(func() { backgroundCLI(t, "image", "rm", freshID); backgroundCLI(t, "image", "rm", old.Config.Image) })
	save("two")
	backgroundSuccess(t, "project", "up", "--config", file, "--recreate", "app")
	if len(history()) != 2 || inspectBackground(t, ref).Config.Image != freshID {
		t.Fatal("new deployment not recorded")
	}
	os.Remove(file)
	os.Remove(envfile)
	backgroundSuccess(t, "project", "rollback", "--wait", name, "1")
	restored := inspectBackground(t, ref)
	if restored.Config.Image != old.Config.Image {
		t.Fatal("rollback followed mutable tag")
	}
	if got := backgroundSuccess(t, "exec", ref, "--", "/bin/sh", "-c", "printf '%s:' \"$APP_VERSION\"; cat /data/value"); got != "one:persistent\n" {
		t.Fatalf("configuration/data: %q", got)
	}
	if h := history(); len(h) != 3 || h[2].RestoredFrom != 1 {
		t.Fatal("rollback provenance missing")
	}
	run["command"] = []string{"/missing-command"}
	save("broken")
	if code, _, _ := backgroundCLI(t, "project", "up", "--config", file, "--recreate", "app"); code == 0 {
		t.Fatal("bad replacement succeeded")
	}
	if len(history()) != 3 {
		t.Fatal("failed deployment recorded as success")
	}
	backgroundSuccess(t, "project", "rollback", name, "1")
	if got := backgroundSuccess(t, "exec", ref, "--", "/bin/sh", "-c", "echo $APP_VERSION"); got != "one\n" {
		t.Fatalf("recovery: %q", got)
	}
	before := inspectBackground(t, ref).ID
	backgroundSuccess(t, "image", "rm", freshID)
	if code, _, _ := backgroundCLI(t, "project", "rollback", name, "2"); code == 0 {
		t.Fatal("missing pinned image accepted")
	}
	if inspectBackground(t, ref).ID != before {
		t.Fatal("missing image removed live service")
	}
	if code, _, _ := backgroundCLI(t, "project", "rollback", name, "999"); code == 0 {
		t.Fatal("missing revision accepted")
	}
	backgroundSuccess(t, "project", "down", "--volumes", name)
}

func TestProjectRollbackRerunsTasksRemovesOrphansAndRefreshesSecrets(t *testing.T) {
	require(t)
	t.Setenv("CASKLET_MACHINE_STATE_DIR", t.TempDir())
	name := fmt.Sprintf("history-task-%x", time.Now().UnixNano())
	dir := t.TempDir()
	file := filepath.Join(dir, "project.json")
	secret := filepath.Join(dir, "secret")
	os.WriteFile(secret, []byte("first"), 0600)
	task := map[string]any{"task": true, "run": map[string]any{"rootfs": template, "network": "none", "mount": []string{"type=volume,source=data,target=/data"}, "command": []string{"/bin/sh", "-c", "echo migration >> /data/runs"}}}
	app := map[string]any{"depends-on": map[string]string{"migrate": "completed"}, "run": map[string]any{"rootfs": template, "network": "none", "mount": []string{"type=volume,source=data,target=/data"}, "secret": []string{"source=secret,target=token"}, "command": []string{"/bin/sleep", "300"}}}
	services := map[string]any{"migrate": task, "app": app}
	doc := map[string]any{"name": name, "networks": []string{}, "volumes": []string{"data"}, "services": services}
	save := func() {
		t.Helper()
		data, _ := json.Marshal(doc)
		if err := os.WriteFile(file, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { backgroundCLI(t, "project", "down", "--volumes", name) })
	save()
	backgroundSuccess(t, "project", "up", "--config", file)
	services["extra"] = map[string]any{"run": map[string]any{"rootfs": template, "network": "none", "command": []string{"/bin/sleep", "300"}}}
	save()
	backgroundSuccess(t, "project", "up", "--config", file)
	os.WriteFile(secret, []byte("rotated"), 0600)
	backgroundSuccess(t, "project", "rollback", name, "1")
	ref := "cp-" + name + "-app"
	if got := backgroundSuccess(t, "exec", ref, "--", "/bin/sh", "-c", "wc -l < /data/runs; cat /run/secrets/token"); got != "2\nrotated" {
		t.Fatalf("task/secret/data rollback: %q", got)
	}
	if code, _, _ := backgroundCLI(t, "inspect", "cp-"+name+"-extra"); code == 0 {
		t.Fatal("rollback retained absent service")
	}
	backgroundSuccess(t, "project", "down", "--volumes", name)
}
