package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/mingo-liu/casklet/internal/project"
)

func TestProjectTaskConfigAndExitStatus(t *testing.T) {
	dir := t.TempDir()
	filename := filepath.Join(dir, "project.json")
	good := `{"name":"demo","services":{"migrate":{"task":true,"run":{"image":"app:1"}},"api":{"run":{"image":"app:1"},"depends-on":{"migrate":"completed"}}}}`
	if err := os.WriteFile(filename, []byte(good), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := Parse([]string{"project", "up", "--config", filename})
	if err != nil || !r.Project.HasTasks() || r.Project.Services[0].Config.Labels[project.LabelTask] != "true" {
		t.Fatalf("task %+v %v", r.Project, err)
	}
	for _, run := range []string{`"restart":"always"`, `"health-cmd":"true"`, `"health-interval":"1s"`} {
		body := `{"name":"demo","services":{"task":{"task":true,"run":{"image":"app:1",` + run + `}}}}`
		os.WriteFile(filename, []byte(body), 0600)
		if _, err := Parse([]string{"project", "validate", "--config", filename}); err == nil {
			t.Fatalf("accepted %s", run)
		}
	}
	var output error = &project.TaskFailure{Service: "migrate", Code: 7}
	if !errors.As(output, new(*project.TaskFailure)) {
		t.Fatal("task error type")
	}
	code := manageOperation(Request{Action: "project-up"}, os.Stderr, func(_ context.Context) (int, error) { return 125, output })
	if code != 7 {
		t.Fatalf("task exit %d", code)
	}
}
