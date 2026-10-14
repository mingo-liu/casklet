package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mingo-liu/casklet/internal/container"
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

func TestProjectAliasFingerprintIgnoresOrderAndDetectsChanges(t *testing.T) {
	service := func(aliases string) project.Service {
		t.Helper()
		file := projectConfigFile(t, `{"name":"demo","services":{"app":{"run":{"image":"app:1","network-alias":`+aliases+`}}}}`)
		request, err := Parse([]string{"project", "up", "--config", file})
		if err != nil {
			t.Fatal(err)
		}
		return request.Project.Services[0]
	}
	first := service(`["web","backend"]`)
	for _, aliases := range []string{`["backend","web"]`, `["app","web","backend"]`} {
		if other := service(aliases); other.Fingerprint != first.Fingerprint {
			t.Fatalf("equivalent aliases %s have different fingerprints", aliases)
		}
	}
	for _, aliases := range []string{`["web"]`, `["web","backend","extra"]`} {
		if other := service(aliases); other.Fingerprint == first.Fingerprint {
			t.Fatalf("changed aliases %s have the same fingerprint", aliases)
		}
	}
	// Fingerprint normalization must leave the generated run arguments intact.
	request, err := Parse(first.Args)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(request.Config.NetworkAliases, []string{"web", "backend", "app"}) {
		t.Fatalf("run alias order changed: %v", request.Config.NetworkAliases)
	}
}

func TestProjectLegacyAliasFingerprintReusesServicesAndRejectsDrift(t *testing.T) {
	// This hash was generated before alias order was normalized, with explicit
	// aliases web/backend followed by the automatically added service alias app.
	const legacyFingerprint = "eb2ce534958b53f25d0e707e930104a3fcb722e8133cc7ad94678b90b28b8d61"
	for _, test := range []struct {
		name         string
		fields       string
		owner        string
		inspectionID string
		inspectError bool
		canonical    bool
		stopped      bool
		wantError    bool
	}{
		{name: "unchanged", fields: `"network-alias":["web","backend"]`},
		{name: "reordered", fields: `"network-alias":["backend","web"]`},
		{name: "service alias reordered", fields: `"network-alias":["app","backend","web"]`},
		{name: "canonical fast path", fields: `"network-alias":["backend","web"]`, canonical: true},
		{name: "stopped", fields: `"network-alias":["backend","web"]`, stopped: true},
		{name: "added alias", fields: `"network-alias":["backend","web","extra"]`, wantError: true},
		{name: "removed alias", fields: `"network-alias":["web"]`, wantError: true},
		{name: "changed environment", fields: `"network-alias":["backend","web"],"env":["MODE=changed"]`, wantError: true},
		{name: "changed command", fields: `"network-alias":["backend","web"],"command":["/bin/true"]`, wantError: true},
		{name: "wrong ownership", fields: `"network-alias":["backend","web"]`, owner: "other", wantError: true},
		{name: "wrong inspected identity", fields: `"network-alias":["backend","web"]`, inspectionID: "other", wantError: true},
		{name: "inspection failure", fields: `"network-alias":["backend","web"]`, inspectError: true, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			file := projectConfigFile(t, `{"name":"demo","services":{"app":{"run":{"image":"app:1",`+test.fields+`}}}}`)
			request, err := Parse([]string{"project", "up", "--config", file})
			if err != nil {
				t.Fatal(err)
			}
			record := container.Record{ID: strings.Repeat("1", 32), Name: "cp-demo-app", State: container.StateRunning,
				Labels: map[string]string{project.LabelName: "demo", project.LabelService: "app", project.LabelSpec: legacyFingerprint}}
			if test.canonical {
				record.Labels[project.LabelSpec] = request.Project.Services[0].Fingerprint
			}
			savedFingerprint := record.Labels[project.LabelSpec]
			if test.owner != "" {
				record.Labels[project.LabelName] = test.owner
			}
			if test.stopped {
				record.State = container.StateExited
			}
			network := project.Resource{Kind: "network", Name: "cp-demo-default", CreatedAt: time.Unix(100, 0).UTC()}
			var mutations []string
			call := func(ctx context.Context, args ...string) ([]byte, error) {
				switch args[0] {
				case "ps":
					return json.Marshal([]container.Record{record})
				case "inspect":
					if test.canonical || test.owner != "" || args[1] != record.ID {
						t.Fatalf("inspected unowned or wrong container: %v", args)
					}
					if test.inspectError {
						return nil, errors.New("inspection failed")
					}
					id := record.ID
					if test.inspectionID != "" {
						id = test.inspectionID
					}
					return json.Marshal(container.Inspection{ID: id, Config: container.InspectionConfig{
						NetworkAliases: []string{"web", "backend", "app", "cp-demo-app"}}})
				case "network":
					return json.Marshal([]project.Resource{network})
				case "volume":
					return []byte("[]"), nil
				case "start":
					mutations = append(mutations, "start")
					if !test.stopped || args[1] != record.ID {
						t.Fatalf("unexpected start: %v", args)
					}
					record.State = container.StateRunning
					return []byte(record.ID), nil
				default:
					mutations = append(mutations, args[0])
					return nil, errors.New("unexpected mutation")
				}
			}
			runner := project.Runner{Call: call, State: project.State{Version: 1, Name: "demo",
				Resources: map[string]project.Resource{"network:cp-demo-default": network}}, Save: func(project.State) error {
				t.Fatal("reused resources must not rewrite ownership state")
				return nil
			}}
			records, err := runner.Up(context.Background(), request.Project, project.UpOptions{Timeout: time.Second})
			if (err != nil) != test.wantError {
				t.Fatalf("up returned %v, want error %v", err, test.wantError)
			}
			if !test.wantError && (len(records) != 1 || records[0].ID != record.ID || records[0].Labels[project.LabelSpec] != savedFingerprint) {
				t.Fatalf("legacy service was not preserved: %v", records)
			}
			if test.stopped {
				if !reflect.DeepEqual(mutations, []string{"start"}) {
					t.Fatalf("stopped service was not resumed: %v", mutations)
				}
			} else if len(mutations) != 0 {
				t.Fatalf("existing service or resources mutated: %v", mutations)
			}
		})
	}
}

func TestProjectLegacyAliasFingerprintPreservesExplicitContainerNameAlias(t *testing.T) {
	for _, aliases := range []string{`["cp-demo-app","web","backend"]`, `["web","backend","app","cp-demo-app"]`} {
		t.Run(aliases, func(t *testing.T) {
			file := projectConfigFile(t, `{"name":"demo","services":{"app":{"run":{"image":"app:1","network-alias":`+aliases+`}}}}`)
			request, err := Parse([]string{"project", "up", "--config", file})
			if err != nil {
				t.Fatal(err)
			}
			service := request.Project.Services[0]
			original, err := Parse(service.Args)
			if err != nil {
				t.Fatal(err)
			}
			for _, label := range []string{project.LabelName, project.LabelService, project.LabelSpec} {
				delete(original.Config.Labels, label)
			}
			legacy := projectFingerprint(original.Config, original.Entrypoint)
			savedAliases := slices.Clone(original.Config.NetworkAliases)
			if !service.MatchLegacyFingerprint(legacy, savedAliases) {
				t.Fatal("explicit container name alias was treated as an implicit store alias")
			}
			if !reflect.DeepEqual(savedAliases, original.Config.NetworkAliases) {
				t.Fatal("legacy verification modified saved aliases")
			}
		})
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

func TestProjectWaitOptionsAndHelp(t *testing.T) {
	file := projectConfigFile(t, `{"name":"demo","services":{"api":{"run":{"image":"app:1"}}}}`)
	for _, wait := range []bool{false, true} {
		args := []string{"project", "up", "--config", file, "--timeout", "2s"}
		if wait {
			args = append(args, "--wait")
		}
		request, err := Parse(args)
		if err != nil || request.Wait != wait || request.WaitTimeout != 2*time.Second {
			t.Fatalf("parse=%+v err=%v", request, err)
		}
	}
	for _, args := range [][]string{{"project", "down", "--wait", "demo"}, {"project", "ps", "--wait", "demo"}, {"project", "up", "--wait", "--timeout", "0s", "--config", file}} {
		if _, err := Parse(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	for _, mac := range []bool{false, true} {
		text, err := scopedUsage("project up", mac)
		for _, want := range []string{"--wait", "whole deployment", "124", "without a healthcheck", "generation"} {
			if err != nil || !strings.Contains(text, want) {
				t.Fatalf("help missing %q: %v %s", want, err, text)
			}
		}
	}
	out, diagnostic, code := helpExecution(t, []string{"project", "up", "--wait", "--help"})
	equivalent, _, other := helpExecution(t, []string{"help", "project", "up"})
	if code != 0 || other != 0 || out != equivalent || diagnostic != "" {
		t.Fatalf("help mismatch %d %d %s", code, other, diagnostic)
	}
}

func TestProjectWaitTimeoutExitStatus(t *testing.T) {
	for _, tt := range []struct {
		wait bool
		err  error
		code int
	}{
		{true, project.ErrWaitTimeout, 124},
		{false, project.ErrWaitTimeout, 125},
		{true, context.DeadlineExceeded, 125},
	} {
		var diagnostic strings.Builder
		code := manageOperation(Request{Action: "project-up", Wait: tt.wait}, &diagnostic, func(context.Context) (int, error) { return 0, tt.err })
		if code != tt.code {
			t.Fatalf("wait=%v err=%v code=%d", tt.wait, tt.err, code)
		}
	}
}

func TestProjectWaitDeadlineIncludesLocalProjectLock(t *testing.T) {
	t.Setenv("CASKLET_MACHINE_STATE_DIR", t.TempDir())
	root, err := project.DefaultRoot()
	if err != nil {
		t.Fatal(err)
	}
	locked, err := project.Open(context.Background(), root, "demo")
	if err != nil {
		t.Fatal(err)
	}
	defer locked.Close()
	var out, diagnostic strings.Builder
	code := executeProject(Request{Action: "project-up", Project: project.Plan{Name: "demo"}, Wait: true, WaitTimeout: 20 * time.Millisecond}, &out, &diagnostic)
	if code != 124 || out.Len() != 0 || !strings.Contains(diagnostic.String(), "project readiness") {
		t.Fatalf("lock deadline code=%d output=%s diagnostic=%s", code, out.String(), diagnostic.String())
	}
}

func TestProjectExternalVolumeMountsUseActualNameAndStableFingerprint(t *testing.T) {
	plan := func(logical, actual string) project.Plan {
		t.Helper()
		body := `{"name":"demo","external-volumes":{"` + logical + `":"` + actual + `"},"services":{"app":{"run":{"image":"app:1","mount":["type=volume,source=` + logical + `,target=/data,readonly"],"command":["/bin/sleep","30"]}}}}`
		request, err := Parse([]string{"project", "up", "--config", projectConfigFile(t, body)})
		if err != nil {
			t.Fatal(err)
		}
		return request.Project
	}
	first := plan("data", "Restored_Data.1")
	if len(first.Volumes) != 0 || !reflect.DeepEqual(first.ExternalVolumes, []string{"Restored_Data.1"}) {
		t.Fatalf("external volume plan %+v", first)
	}
	request, err := Parse(first.Services[0].Args)
	if err != nil || request.Config.Mounts[0].Source != "Restored_Data.1" || !request.Config.Mounts[0].ReadOnly {
		t.Fatalf("actual mount %+v %v", request.Config.Mounts, err)
	}
	if first.Services[0].Fingerprint != plan("renamed", "Restored_Data.1").Services[0].Fingerprint {
		t.Fatal("logical name affects mount identity")
	}
	if first.Services[0].Fingerprint == plan("data", "Restored_Data.2").Services[0].Fingerprint {
		t.Fatal("changed external target does not affect identity")
	}
}

func TestProjectExternalVolumeHelp(t *testing.T) {
	for _, mac := range []bool{false, true} {
		up, err := scopedUsage("project up", mac)
		for _, want := range []string{"external-volumes", "never created or deleted", "releases"} {
			if err != nil || !strings.Contains(up, want) {
				t.Fatalf("up help lacks %q: %v %s", want, err, up)
			}
		}
		down, err := scopedUsage("project down", mac)
		if err != nil || !strings.Contains(down, "External volumes survive even with --volumes") {
			t.Fatalf("down help: %v %s", err, down)
		}
	}
	for _, topic := range []string{"up", "down"} {
		out, _, code := helpExecution(t, []string{"project", topic, "--help"})
		other, _, otherCode := helpExecution(t, []string{"help", "project", topic})
		if code != 0 || otherCode != 0 || out != other {
			t.Fatalf("help mismatch for %s", topic)
		}
	}
}
