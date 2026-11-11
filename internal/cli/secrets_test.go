package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunAndProjectSecretFilesStayAsReferences(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "password")
	if err := os.WriteFile(source, []byte("private-password"), 0600); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "run.json")
	if err := os.WriteFile(file, []byte(`{"rootfs":"builtin:busybox","secret":["source=password,target=db"],"command":["/bin/true"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	request, err := Parse([]string{"run", "--config", file})
	if err != nil || len(request.Config.Secrets) != 1 || request.Config.Secrets[0].Source != source || len(request.Config.Env) != 0 {
		t.Fatalf("config: %+v %v", request, err)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	if _, err := Parse([]string{"run", "--config", file}); err != nil {
		t.Fatalf("parser reopened contents: %v", err)
	}
	projectFile := filepath.Join(dir, "project.json")
	if err := os.WriteFile(projectFile, []byte(`{"name":"demo","networks":[],"services":{"app":{"run":{"rootfs":"/template","network":"none","secret":["source=password,target=db"],"command":["/bin/true"]}}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	project, err := Parse([]string{"project", "up", "--config", projectFile})
	if err != nil || project.Project.Services[0].Config.Secrets[0].Source != source || strings.Contains(strings.Join(project.Project.Services[0].Args, " "), "private-password") {
		t.Fatalf("project: %+v %v", project, err)
	}
}
