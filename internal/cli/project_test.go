package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mingo-liu/casklet/internal/project"
)

func projectConfigFile(t *testing.T, body string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "project.json")
	if err := os.WriteFile(file, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestProjectPlanExpandsFilesScopesResourcesAndFingerprintsConfig(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "project.json")
	env := filepath.Join(dir, "app.env")
	if err := os.WriteFile(env, []byte("MODE=one\n"), 0600); err != nil {
		t.Fatal(err)
	}
	body := `{"name":"demo","volumes":["data"],"services":{"api":{"run":{"image":"app:1","env-file":["app.env"],"mount":["type=volume,source=data,target=/data"]},"depends-on":{"db":"healthy"}},"db":{"run":{"image":"db:1"}}}}`
	if err := os.WriteFile(file, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	first, err := Parse([]string{"project", "up", "--config", file})
	if err != nil {
		t.Fatal(err)
	}
	if first.Project.Services[0].Key != "db" || first.Project.Services[1].Key != "api" {
		t.Fatalf("order %v", first.Project)
	}
	api, err := Parse(first.Project.Services[1].Args)
	if err != nil {
		t.Fatal(err)
	}
	if !api.Detach || api.Name != "cp-demo-api" || api.Config.Network != "cp-demo-default" || api.Config.Mounts[0].Source != "cp-demo-data" || api.Config.Labels[project.LabelName] != "demo" {
		t.Fatalf("expanded %v", api)
	}
	for _, arg := range first.Project.Services[1].Args {
		if strings.Contains(arg, "env-file") || strings.Contains(arg, "project.json") {
			t.Fatalf("local file leaked: %q", arg)
		}
	}
	again, err := Parse([]string{"project", "up", "--config", file})
	if err != nil || again.Project.Services[1].Fingerprint != first.Project.Services[1].Fingerprint {
		t.Fatalf("unstable fingerprint %v", err)
	}
	os.WriteFile(env, []byte("MODE=two\n"), 0600)
	changed, err := Parse([]string{"project", "up", "--config", file})
	if err != nil || changed.Project.Services[1].Fingerprint == first.Project.Services[1].Fingerprint {
		t.Fatalf("env drift undetected %v", err)
	}
	data, _ := json.Marshal(changed.Project.Services[1].Fingerprint)
	if strings.Contains(string(data), "MODE") {
		t.Fatal("fingerprint exposes environment")
	}
}

func TestProjectRejectsInvalidServiceConfigurationBeforeRuntime(t *testing.T) {
	for _, run := range []string{`{"image":"app:1","name":"manual"}`, `{"image":"app:1","detach":true}`, `{"image":"app:1","network":"undeclared"}`, `{"image":"app:1","mount":["type=volume,source=missing,target=/data"]}`, `{"image":"app:1","label":["io.casklet.project.name=other"]}`, `{"image":"app:1","env":["BAD-KEY=secret-value"]}`, `{"image":"app:1","unknown":true}`} {
		file := projectConfigFile(t, `{"name":"demo","services":{"api":{"run":`+run+`}}}`)
		_, err := Parse([]string{"project", "up", "--config", file})
		if err == nil || strings.Contains(err.Error(), "secret-value") {
			t.Fatalf("invalid config %s: %v", run, err)
		}
	}
	for _, args := range [][]string{{"project"}, {"project", "up"}, {"project", "up", "--timeout", "0s", "--config", "missing"}, {"project", "down"}, {"project", "ps", "demo", "--json"}, {"__project-resource", "rm", "volume", "demo", "bad"}} {
		if _, err := Parse(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestProjectEnvironmentFingerprintKeepsImageOverridesDistinct(t *testing.T) {
	fingerprint := func(env string) string {
		file := projectConfigFile(t, `{"name":"demo","services":{"app":{"run":{"image":"app:1","env":`+env+`}}}}`)
		request, err := Parse([]string{"project", "up", "--config", file})
		if err != nil {
			t.Fatal(err)
		}
		return request.Project.Services[0].Fingerprint
	}
	if fingerprint(`[]`) == fingerprint(`["PATH=/bin:/usr/bin"]`) {
		t.Fatal("explicit image override equals absence")
	}
	if fingerprint(`["B=two","A=old","A=one"]`) != fingerprint(`["A=one","B=two"]`) {
		t.Fatal("equivalent explicit environment has different identity")
	}
}

func TestProjectRejectsAliasConflictsAndImpossibleHealthDependencies(t *testing.T) {
	for _, body := range []string{
		`{"name":"demo","services":{"a":{"run":{"image":"app:1","network-alias":["b"]}},"b":{"run":{"image":"app:1"}}}}`,
		`{"name":"demo","services":{"a":{"run":{"image":"app:1","no-healthcheck":true}},"b":{"run":{"image":"app:1"},"depends-on":{"a":"healthy"}}}}`,
		`{"name":"demo","services":{"a":{"run":{"image":"app:1","publish":["127.0.0.1:8080:80"]}},"b":{"run":{"image":"app:1","publish":["0.0.0.0:8080:80"]}}}}`,
	} {
		if _, err := Parse([]string{"project", "up", "--config", projectConfigFile(t, body)}); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
}
