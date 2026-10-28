package cli

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mingo-liu/casklet/internal/project"
)

func TestRestoreDeploymentPinsImagesAndKeepsExpandedEnvironment(t *testing.T) {
	file := filepath.Join(t.TempDir(), "project.json")
	data := `{"name":"demo","networks":[],"services":{"app":{"run":{"image":"example.com/app:latest","network":"none","env":["TOKEN=private-token"],"command":["/bin/sleep","300"]}}}}`
	if err := os.WriteFile(file, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	plan, err := readProjectPlan(file)
	if err != nil {
		t.Fatal(err)
	}
	os.Remove(file)
	id := "sha256:" + strings.Repeat("a", 64)
	d := project.Deployment{Revision: 1, CreatedAt: time.Now(), Plan: plan, Images: map[string]string{"app": id}}
	restored, err := restoreDeployment(d)
	if err != nil {
		t.Fatal(err)
	}
	request, err := Parse(restored.Services[0].Args)
	if err != nil || request.Config.Image != id || request.Config.Labels[project.LabelSpec] != restored.Services[0].Fingerprint || request.Config.Env[0] != "TOKEN=private-token" {
		t.Fatalf("restored: %+v %v", request, err)
	}
	// History is immutable and does not alias the restored plan.
	if plan.Services[0].Config.Image != "example.com/app:latest" || restored.Services[0].Fingerprint == plan.Services[0].Fingerprint {
		t.Fatal("snapshot mutated or spec not pinned")
	}
	invalid := d
	invalid.Images = map[string]string{}
	if _, err := restoreDeployment(invalid); err == nil {
		t.Fatal("missing image identity accepted")
	}
	saved, _ := json.Marshal(d)
	var edited project.Deployment
	json.Unmarshal(saved, &edited)
	edited.Plan.Services[0].Args = append([]string{"run", "--env-file", "/private/missing"}, edited.Plan.Services[0].Args[1:]...)
	if _, err := restoreDeployment(edited); err == nil || !strings.Contains(err.Error(), "unexpanded") {
		t.Fatalf("reopened saved file: %v", err)
	}
	edited = d
	edited.Plan.Services = append([]project.Service(nil), d.Plan.Services...)
	edited.Plan.Services[0].Config.Memory++
	if _, err := restoreDeployment(edited); err == nil {
		t.Fatal("changed defaults accepted")
	}
	for _, args := range [][]string{{"project", "history", "--json", "demo"}, {"project", "rollback", "--wait", "demo", "1"}} {
		if _, err := Parse(args); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"project", "rollback", "demo"}, {"project", "rollback", "demo", "0"}, {"project", "rollback", "demo", "-1"}, {"project", "rollback", "--timeout", "0s", "demo", "1"}} {
		if _, err := Parse(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestRestoreDefaultNetworkTaskAndEmptyEnvironment(t *testing.T) {
	file := filepath.Join(t.TempDir(), "project.json")
	data := `{"name":"demo","services":{"migrate":{"task":true,"run":{"rootfs":"/template","command":["/bin/true"]}},"app":{"depends-on":{"migrate":"completed"},"run":{"rootfs":"/template","network-alias":["web","backend"],"command":["/bin/sleep","300"]}}}}`
	os.WriteFile(file, []byte(data), 0600)
	plan, err := readProjectPlan(file)
	if err != nil {
		t.Fatal(err)
	}
	_, err = restoreDeployment(project.Deployment{Plan: plan, Images: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRollbackDeadlineRetainsTimeoutStatus(t *testing.T) {
	code := manageOperation(Request{Action: "project-rollback"}, io.Discard, func(context.Context) (int, error) { return 125, project.ErrWaitTimeout })
	if code != 124 {
		t.Fatalf("rollback timeout status: %d", code)
	}
}

func TestProjectHistoryWorksWithoutRuntimeTools(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("CASKLET_MACHINE_STATE_DIR", t.TempDir())
	var output strings.Builder
	code, err := executeProjectContext(context.Background(), Request{Action: "project-history", Reference: "demo", JSON: true}, &output, io.Discard)
	if err != nil || code != 0 || output.String() != "[]\n" {
		t.Fatalf("local history: %d %q %v", code, output.String(), err)
	}
}
