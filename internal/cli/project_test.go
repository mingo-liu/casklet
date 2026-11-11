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
	if err := os.WriteFile(env, []byte("MODE=two\n"), 0600); err != nil {
		t.Fatal(err)
	}
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

func TestProjectLegacyAliasFingerprintReusesEquivalentConfigurations(t *testing.T) {
	tests := []struct {
		name   string
		fields string
	}{
		{name: "unchanged", fields: `"network-alias":["web","backend"]`},
		{name: "reordered", fields: `"network-alias":["backend","web"]`},
		{name: "service alias reordered", fields: `"network-alias":["app","backend","web"]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newLegacyProjectFixture(t, tt.fields)
			assertLegacyProjectReuse(t, fixture, nil)
		})
	}
}

func TestProjectCanonicalFingerprintSkipsLegacyInspection(t *testing.T) {
	fixture := newLegacyProjectFixture(t, `"network-alias":["backend","web"]`)
	fixture.record.Labels[project.LabelSpec] = fixture.request.Project.Services[0].Fingerprint
	assertLegacyProjectReuse(t, fixture, nil)
	if fixture.inspections != 0 {
		t.Fatal("canonical fingerprint performed legacy inspection")
	}
}

func TestProjectLegacyFingerprintStartsStoppedService(t *testing.T) {
	fixture := newLegacyProjectFixture(t, `"network-alias":["backend","web"]`)
	fixture.record.State = container.StateExited
	assertLegacyProjectReuse(t, fixture, []string{"start"})
}

func TestProjectLegacyFingerprintRejectsConfigurationDrift(t *testing.T) {
	tests := []struct {
		name   string
		fields string
	}{
		{name: "added alias", fields: `"network-alias":["backend","web","extra"]`},
		{name: "removed alias", fields: `"network-alias":["web"]`},
		{name: "changed environment", fields: `"network-alias":["backend","web"],"env":["MODE=changed"]`},
		{name: "changed command", fields: `"network-alias":["backend","web"],"command":["/bin/true"]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newLegacyProjectFixture(t, tt.fields)
			if _, err := fixture.runner.Up(context.Background(), fixture.request.Project, project.UpOptions{Timeout: time.Second}); err == nil {
				t.Fatal("accepted changed legacy configuration")
			}
			if len(fixture.mutations) != 0 || fixture.saved.ID != "" {
				t.Fatal("configuration drift changed resources or comparison history")
			}
		})
	}
}

func TestProjectLegacyFingerprintRejectsUnownedServiceBeforeInspection(t *testing.T) {
	fixture := newLegacyProjectFixture(t, `"network-alias":["backend","web"]`)
	fixture.record.Labels[project.LabelName] = "other"
	if _, err := fixture.runner.Up(context.Background(), fixture.request.Project, project.UpOptions{Timeout: time.Second}); err == nil {
		t.Fatal("accepted unowned legacy service")
	}
	if fixture.inspections != 0 || len(fixture.mutations) != 0 {
		t.Fatal("inspected or mutated unowned service")
	}
}

func TestProjectLegacyFingerprintRejectsChangedInspectionIdentity(t *testing.T) {
	fixture := newLegacyProjectFixture(t, `"network-alias":["backend","web"]`)
	fixture.inspection.ID = "other"
	if _, err := fixture.runner.Up(context.Background(), fixture.request.Project, project.UpOptions{Timeout: time.Second}); err == nil {
		t.Fatal("accepted changed inspection identity")
	}
	if len(fixture.mutations) != 0 {
		t.Fatal("changed inspection identity reached mutations")
	}
}

func TestProjectLegacyFingerprintPreservesInspectionFailure(t *testing.T) {
	fixture := newLegacyProjectFixture(t, `"network-alias":["backend","web"]`)
	fixture.inspectionErr = errors.New("inspection failed")
	if _, err := fixture.runner.Up(context.Background(), fixture.request.Project, project.UpOptions{Timeout: time.Second}); !errors.Is(err, fixture.inspectionErr) {
		t.Fatalf("lost inspection failure: %v", err)
	}
	if len(fixture.mutations) != 0 {
		t.Fatal("inspection failure reached mutations")
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
	tests := []struct {
		wait bool
		err  error
		code int
	}{
		{true, project.ErrWaitTimeout, 124},
		{false, project.ErrWaitTimeout, 125},
		{true, context.DeadlineExceeded, 125},
	}
	for _, tt := range tests {
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
	defer func() { _ = locked.Close() }()
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

func TestProjectRecreateAndPreviewParsingAndHelp(t *testing.T) {
	file := projectConfigFile(t, `{"name":"demo","services":{"api":{"run":{"image":"app:1"}},"db":{"run":{"image":"db:1"}}}}`)
	request, err := Parse([]string{"project", "up", "--config", file, "--recreate", "api", "--recreate=db", "--remove-orphans", "--dry-run", "--json"})
	if err != nil || !request.DryRun || !request.JSON || !request.RemoveOrphans || len(request.Recreate) != 2 {
		t.Fatalf("request=%+v err=%v", request, err)
	}
	for _, names := range [][]string{{"--recreate", "missing"}, {"--recreate", "api", "--recreate", "api"}, {"--recreate", ""}} {
		if _, err := Parse(append([]string{"project", "up", "--config", file}, names...)); err == nil {
			t.Fatalf("accepted %v", names)
		}
	}
	direct, err := Parse([]string{"project", "up", "--help"})
	if err != nil {
		t.Fatal(err)
	}
	alternate, err := Parse([]string{"help", "project", "up"})
	if err != nil || direct.HelpTopic != alternate.HelpTopic {
		t.Fatalf("help parity: %v %v", direct, err)
	}
	help, err := platformUsage(direct.HelpTopic)
	if err != nil {
		t.Fatal(err)
	}
	for _, term := range []string{"--recreate", "--remove-orphans", "service_removed", "--dry-run", "private root", "no automatic", "running VM", "missing services"} {
		if !strings.Contains(help, term) {
			t.Errorf("missing help %q", term)
		}
	}
	// Initial durable executions use generation zero.
	if _, err := Parse([]string{"__project-replace", strings.Repeat("0", 32), "0", "demo", "api", "spec"}); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"id", "0", "demo", "api", "spec"}, {strings.Repeat("0", 32), "-1", "demo", "api", "spec"}} {
		if _, err := parseProjectReplace(args); err == nil {
			t.Fatalf("accepted identity %v", args)
		}
	}
}

func projectConfigFile(t *testing.T, body string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "project.json")
	if err := os.WriteFile(file, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return file
}

type legacyProjectFixture struct {
	request       Request
	record        container.Record
	inspection    container.Inspection
	inspectionErr error
	inspections   int
	mutations     []string
	saved         project.ServiceReceipt
	runner        project.Runner
}

func newLegacyProjectFixture(t *testing.T, fields string) *legacyProjectFixture {
	t.Helper()
	// Generated before alias normalization with web/backend followed by app.
	const legacyFingerprint = "eb2ce534958b53f25d0e707e930104a3fcb722e8133cc7ad94678b90b28b8d61"
	file := projectConfigFile(t, `{"name":"demo","services":{"app":{"run":{"image":"app:1",`+fields+`}}}}`)
	request, err := Parse([]string{"project", "up", "--config", file})
	if err != nil {
		t.Fatal(err)
	}
	fixture := &legacyProjectFixture{
		request: request,
		record: container.Record{ID: strings.Repeat("1", 32), Name: "cp-demo-app", State: container.StateRunning,
			Labels: map[string]string{project.LabelName: "demo", project.LabelService: "app", project.LabelSpec: legacyFingerprint}},
	}
	fixture.inspection = container.Inspection{ID: fixture.record.ID, Config: container.InspectionConfig{
		NetworkAliases: []string{"web", "backend", "app", "cp-demo-app"}}}
	network := project.Resource{Kind: "network", Name: "cp-demo-default", CreatedAt: time.Unix(100, 0).UTC()}
	call := func(ctx context.Context, args ...string) ([]byte, error) {
		switch args[0] {
		case "ps":
			return json.Marshal([]container.Record{fixture.record})
		case "inspect":
			fixture.inspections++
			if args[1] != fixture.record.ID {
				t.Fatalf("inspected wrong container: %v", args)
			}
			if fixture.inspectionErr != nil {
				return nil, fixture.inspectionErr
			}
			return json.Marshal(fixture.inspection)
		case "network":
			return json.Marshal([]project.Resource{network})
		case "volume":
			return []byte("[]"), nil
		case "start":
			fixture.mutations = append(fixture.mutations, "start")
			if fixture.record.State != container.StateExited || args[1] != fixture.record.ID {
				t.Fatalf("unexpected start: %v", args)
			}
			fixture.record.State = container.StateRunning
			return []byte(fixture.record.ID), nil
		default:
			fixture.mutations = append(fixture.mutations, args[0])
			return nil, errors.New("unexpected mutation")
		}
	}
	fixture.runner = project.Runner{
		Backend: project.CommandBackend{Call: call},
		Call:    call,
		State: project.State{Version: 1, Name: "demo",
			Resources: map[string]project.Resource{"network:cp-demo-default": network}},
		Save: func(state project.State) error {
			if !reflect.DeepEqual(state.Resources, map[string]project.Resource{"network:cp-demo-default": network}) {
				t.Fatal("comparison baseline changed resource ownership")
			}
			fixture.saved = state.Services["app"]
			return nil
		},
	}
	return fixture
}

func assertLegacyProjectReuse(t *testing.T, fixture *legacyProjectFixture, wantMutations []string) {
	t.Helper()
	savedFingerprint := fixture.record.Labels[project.LabelSpec]
	records, err := fixture.runner.Up(context.Background(), fixture.request.Project, project.UpOptions{Timeout: time.Second})
	if err != nil || len(records) != 1 || records[0].ID != fixture.record.ID || records[0].Labels[project.LabelSpec] != savedFingerprint {
		t.Fatalf("legacy service was not preserved: %v %v", records, err)
	}
	if fixture.saved.ID != fixture.record.ID || fixture.saved.Spec != savedFingerprint || !reflect.DeepEqual(fixture.saved.Fields, fixture.request.Project.Services[0].Fields) {
		t.Fatalf("incorrect legacy comparison baseline: %+v", fixture.saved)
	}
	if !reflect.DeepEqual(fixture.mutations, wantMutations) {
		t.Fatalf("unexpected mutations: %v", fixture.mutations)
	}
}
