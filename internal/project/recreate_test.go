package project

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mingo-liu/casklet/internal/config"
	"github.com/mingo-liu/casklet/internal/container"
)

func deployedProject(t *testing.T) (*fakeProject, *Runner, Plan) {
	t.Helper()
	fake := newFakeProject()
	runner := fakeRunner(fake)
	plan := fakePlan()
	if _, err := runner.Up(context.Background(), plan, UpOptions{Timeout: time.Second}); err != nil {
		t.Fatal(err)
	}
	fake.mutations = nil
	return fake, runner, plan
}

func TestRecreateSelectedServicePreservesOtherIdentityAndVolumes(t *testing.T) {
	fake, runner, plan := deployedProject(t)
	before, resources := append([]container.Record(nil), fake.records...), cloneResources(fake.resources)
	plan.Services[1].Fingerprint = "updated"
	for i, arg := range plan.Services[1].Args {
		if arg == LabelSpec+"=spec-api" {
			plan.Services[1].Args[i] = LabelSpec + "=updated"
		}
	}
	records, err := runner.Up(context.Background(), plan, UpOptions{Timeout: time.Second, Recreate: []string{"api"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].ID == before[1].ID || records[1].ID != before[0].ID || records[1].Generation != before[0].Generation || !reflect.DeepEqual(resources, fake.resources) {
		t.Fatalf("records/resources changed unexpectedly: %+v / %+v", records, fake.resources)
	}
	if len(fake.mutations) != 2 || fake.mutations[0] != "replace "+before[1].ID {
		t.Fatalf("mutations: %v", fake.mutations)
	}
	// Explicit recreation replaces even identical desired hashes (refreshed tags).
	fake.mutations = nil
	if _, err := runner.Up(context.Background(), plan, UpOptions{Timeout: time.Second, Recreate: []string{"api"}}); err != nil || len(fake.mutations) != 2 {
		t.Fatalf("same config recreation: %v, %v", err, fake.mutations)
	}
}

func cloneResources(values map[string]Resource) map[string]Resource {
	result := map[string]Resource{}
	for key, value := range values {
		result[key] = value
	}
	return result
}

func TestRecreateRejectsUnselectedDriftAndOwnershipBeforeMutation(t *testing.T) {
	for _, scenario := range []string{"drift", "ownership", "missing label", "removed", "duplicate", "unknown", "external", "resource", "source"} {
		t.Run(scenario, func(t *testing.T) {
			fake, runner, plan := deployedProject(t)
			selected := []string{"api"}
			switch scenario {
			case "drift":
				plan.Services[0].Fingerprint = "changed"
			case "ownership":
				fake.records[1].Labels[LabelName] = "other"
			case "missing label":
				delete(fake.records[1].Labels, LabelSpec)
			case "removed":
				plan.Services = plan.Services[1:]
			case "duplicate":
				selected = append(selected, "api")
			case "unknown":
				selected = []string{"unknown"}
			case "external":
				plan.ExternalVolumes = []string{"missing"}
			case "resource":
				delete(runner.State.Resources, "volume:cp-demo-data")
			case "source":
				runner.Call = func(ctx context.Context, args ...string) ([]byte, error) {
					if args[0] == "__project-preflight" {
						return nil, errors.New("source missing")
					}
					return fake.call(ctx, args...)
				}
			}
			if _, err := runner.Up(context.Background(), plan, UpOptions{Timeout: time.Second, Recreate: selected}); err == nil || len(fake.mutations) != 0 {
				t.Fatalf("err=%v mutations=%v", err, fake.mutations)
			}
		})
	}
}

func TestProjectPreviewNeverMutatesOrReleasesOwnership(t *testing.T) {
	fake, runner, plan := deployedProject(t)
	before, saved := cloneResources(runner.State.Resources), fake.saves
	plan.ExternalVolumes = plan.Volumes
	plan.Volumes = nil
	fake.records[0].State = container.StateExited
	if _, err := runner.Up(context.Background(), plan, UpOptions{DryRun: true, Recreate: []string{"api"}}); err != nil {
		t.Fatal(err)
	}
	if len(fake.mutations) != 0 || fake.saves != saved || !reflect.DeepEqual(before, runner.State.Resources) {
		t.Fatalf("preview mutated state: %v", fake.mutations)
	}
	if len(runner.Preview) != 2 || runner.Preview[0].Action != "start" || runner.Preview[1].Action != "recreate" || runner.Preview[1].ID != fake.records[1].ID {
		t.Fatalf("preview=%+v", runner.Preview)
	}
	fresh := fakeRunner(newFakeProject())
	if _, err := fresh.Up(context.Background(), fakePlan(), UpOptions{DryRun: true}); err != nil || len(fresh.Preview) != 4 {
		t.Fatalf("fresh preview=%+v err=%v", fresh.Preview, err)
	}
}

func TestRecreateFailurePreservesUnaffectedAndRetryCreatesMissingService(t *testing.T) {
	fake, runner, plan := deployedProject(t)
	db, resources := fake.records[0], cloneResources(fake.resources)
	fake.failRun = "cp-demo-api"
	if _, err := runner.Up(context.Background(), plan, UpOptions{Recreate: []string{"api"}, Timeout: time.Second}); err == nil {
		t.Fatal("expected failure")
	}
	if len(fake.records) != 1 || fake.records[0].ID != db.ID || !reflect.DeepEqual(resources, fake.resources) {
		t.Fatalf("preservation failed: %+v %+v", fake.records, fake.resources)
	}
	fake.failRun = ""
	if _, err := runner.Up(context.Background(), plan, UpOptions{Recreate: []string{"api"}, Timeout: time.Second}); err != nil {
		t.Fatal(err)
	}
}

func TestRecreateFencesChangedGenerationAndCancellation(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		fake, runner, plan := deployedProject(t)
		ctx, stop := context.WithCancel(context.Background())
		runner.Call = func(ctx context.Context, args ...string) ([]byte, error) {
			if args[0] == "__project-preflight" {
				if cancel {
					stop()
				} else {
					fake.records[1].Generation++
				}
			}
			return fake.call(ctx, args...)
		}
		_, err := runner.Up(ctx, plan, UpOptions{Recreate: []string{"api"}, Timeout: time.Second})
		stop()
		if err == nil || len(fake.records) != 2 || len(fake.mutations) != 0 {
			t.Fatalf("cancel=%v err=%v mutations=%v", cancel, err, fake.mutations)
		}
	}
}

func TestRecreateChecksActualSavedPortAndAliasReservations(t *testing.T) {
	for _, kind := range []string{"port", "alias"} {
		fake, runner, plan := deployedProject(t)
		plan.Services[1].Config.Network = "cp-demo-default"
		plan.Services[1].Config.NetworkAliases = []string{"new"}
		port, _ := config.ParsePortMapping("39123:80")
		plan.Services[1].Config.Publish = []config.PortMapping{port}
		runner.Call = func(ctx context.Context, args ...string) ([]byte, error) {
			if args[0] == "inspect" && args[1] == fake.records[0].ID {
				cfg := container.InspectionConfig{}
				if kind == "port" {
					cfg.Publish = []config.PortMapping{port}
				} else {
					cfg.Network = "cp-demo-default"
					cfg.NetworkAliases = []string{"new"}
				}
				return json.Marshal(container.Inspection{ID: args[1], Generation: fake.records[0].Generation, Config: cfg})
			}
			return fake.call(ctx, args...)
		}
		if _, err := runner.Up(context.Background(), plan, UpOptions{Recreate: []string{"api"}}); err == nil || !strings.Contains(err.Error(), kind) || len(fake.mutations) != 0 {
			t.Fatalf("%s err=%v mutations=%v", kind, err, fake.mutations)
		}
	}
}

func TestRecreateChecksStoppedRetainedSourcesBeforeRemoval(t *testing.T) {
	fake, runner, plan := deployedProject(t)
	fake.records[0].State = container.StateExited
	runner.Call = func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] == "__project-start-preflight" {
			return nil, errors.New("saved bind missing")
		}
		return fake.call(ctx, args...)
	}
	if _, err := runner.Up(context.Background(), plan, UpOptions{Recreate: []string{"api"}}); err == nil || len(fake.mutations) != 0 {
		t.Fatalf("err=%v mutations=%v", err, fake.mutations)
	}
}

func TestPreflightPortExemptionsRequireSameHostScope(t *testing.T) {
	old, _ := config.ParsePortMapping("127.0.0.1:39123:80")
	for _, value := range []string{"0.0.0.0:39123:81", "127.0.0.2:39123:80", "127.0.0.1:39123:80/udp"} {
		args := []string{"run", "--publish", value, "--", "echo", "--publish"}
		got := preflightArguments(args, []config.PortMapping{old})
		if len(got) != len(args) || got[2] != value {
			t.Fatalf("broadened/different binding exempted: %v", got)
		}
	}
	got := preflightArguments([]string{"run", "--publish", "127.0.0.1:39123:81", "--", "echo", "--publish"}, []config.PortMapping{old})
	if len(got) != 4 || got[1] != "--" || got[3] != "--publish" {
		t.Fatalf("same endpoint/workload %v", got)
	}
}

func TestStoppedServicePreflightUsesInspectedSavedHostPorts(t *testing.T) {
	fake, runner, plan := deployedProject(t)
	fake.records[0].State = container.StateExited
	port, _ := config.ParsePortMapping("127.0.0.1:39221:8080")
	checked := false
	runner.Call = func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] == "inspect" && args[1] == fake.records[0].ID {
			return json.Marshal(container.Inspection{ID: args[1], Generation: fake.records[0].Generation, Config: container.InspectionConfig{Publish: []config.PortMapping{port}}})
		}
		if args[0] == "__project-start-preflight" {
			checked = true
			if len(args) != 7 || args[6] != "127.0.0.1:39221:8080/tcp" {
				t.Fatalf("saved port preflight %v", args)
			}
			return nil, errors.New("saved host port occupied")
		}
		return fake.call(ctx, args...)
	}
	if _, err := runner.Up(context.Background(), plan, UpOptions{Recreate: []string{"api"}}); err == nil || !checked || len(fake.mutations) != 0 {
		t.Fatalf("checked=%v err=%v mutations=%v", checked, err, fake.mutations)
	}
}
