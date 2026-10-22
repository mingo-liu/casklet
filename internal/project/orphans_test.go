package project

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mingo-liu/casklet/internal/config"
	"github.com/mingo-liu/casklet/internal/container"
)

func TestRemoveOrphansRequiresExplicitSelectionAndPreservesPreview(t *testing.T) {
	fake, runner, plan := deployedProject(t)
	orphan, kept := fake.records[1], fake.records[0]
	plan.Services = plan.Services[:1]
	before := slices.Clone(fake.records)
	resources, saves := cloneResources(runner.State.Resources), fake.saves
	for _, dryRun := range []bool{false, true} {
		if _, err := runner.Up(context.Background(), plan, UpOptions{DryRun: dryRun}); err == nil || !strings.Contains(err.Error(), "--remove-orphans") {
			t.Fatalf("implicit orphan removal: %v", err)
		}
	}
	if _, err := runner.Up(context.Background(), plan, UpOptions{RemoveOrphans: true, DryRun: true}); err != nil {
		t.Fatal(err)
	}
	if len(runner.Preview) != 2 || !reflect.DeepEqual(runner.Preview[0], Change{Kind: "service", Name: "api", ID: orphan.ID, Action: "remove", Reason: "service_removed"}) {
		t.Fatalf("preview: %+v", runner.Preview)
	}
	if !reflect.DeepEqual(fake.records, before) || len(fake.mutations) != 0 || fake.saves != saves || !reflect.DeepEqual(resources, runner.State.Resources) {
		t.Fatal("preview changed workloads or ownership")
	}
	// Foreign and incomplete labels do not authorize removal.
	foreign := container.Record{ID: strings.Repeat("f", 32), Name: "other", State: container.StateExited, Labels: map[string]string{LabelName: "other", LabelService: "old", LabelSpec: "spec"}}
	unverified := container.Record{ID: strings.Repeat("e", 32), Name: "incomplete", State: container.StateExited, Labels: map[string]string{LabelName: "demo", LabelService: "old"}}
	fake.records = append(fake.records, foreign, unverified)
	got, err := runner.Up(context.Background(), plan, UpOptions{RemoveOrphans: true, Wait: true, Timeout: time.Second})
	if err != nil || len(got) != 1 || got[0].ID != kept.ID || got[0].Generation != kept.Generation {
		t.Fatalf("remaining services: %+v %v", got, err)
	}
	if len(fake.records) != 3 || !reflect.DeepEqual(resources, fake.resources) || !reflect.DeepEqual(fake.mutations, []string{"replace " + orphan.ID}) {
		t.Fatalf("unexpected removal: %+v %v", fake.records, fake.mutations)
	}
	fake.mutations = nil
	if _, err := runner.Up(context.Background(), plan, UpOptions{RemoveOrphans: true}); err != nil || len(fake.mutations) != 0 {
		t.Fatalf("retry: %v %v", err, fake.mutations)
	}
}

func orphanReplacementPlan(plan Plan) Plan {
	service := plan.Services[1]
	service.Key, service.Name, service.Fingerprint = "worker", "cp-demo-worker", "spec-worker"
	service.Args = slices.Clone(service.Args)
	for i, value := range service.Args {
		service.Args[i] = strings.ReplaceAll(value, "api", "worker")
	}
	plan.Services = []Service{plan.Services[0], service}
	return plan
}

func TestRemoveOrphansPreflightsSourcesAndFencesConcurrentChanges(t *testing.T) {
	for _, scenario := range []string{"source", "generation", "ownership", "cancel", "desired drift", "duplicate", "renamed"} {
		t.Run(scenario, func(t *testing.T) {
			fake, runner, plan := deployedProject(t)
			plan = orphanReplacementPlan(plan)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch scenario {
			case "desired drift":
				plan.Services[0].Fingerprint = "changed"
			case "duplicate":
				duplicate := fake.records[1]
				duplicate.ID = strings.Repeat("a", 32)
				fake.records = append(fake.records, duplicate)
			case "renamed":
				fake.records[1].Name = "unexpected"
			}
			runner.Call = func(ctx context.Context, args ...string) ([]byte, error) {
				if args[0] == "__project-preflight" {
					switch scenario {
					case "source":
						return nil, errors.New("source unavailable")
					case "generation":
						fake.records[1].Generation++
					case "ownership":
						fake.records[1].Labels = maps.Clone(fake.records[1].Labels)
						fake.records[1].Labels[LabelSpec] = "changed"
					case "cancel":
						cancel()
					}
				}
				return fake.call(ctx, args...)
			}
			if _, err := runner.Up(ctx, plan, UpOptions{RemoveOrphans: true}); err == nil || len(fake.mutations) != 0 {
				t.Fatalf("err=%v mutations=%v", err, fake.mutations)
			}
		})
	}
}

func TestRemoveOrphansReleasesReservationsBeforeNewServiceStartup(t *testing.T) {
	fake, runner, plan := deployedProject(t)
	orphan := fake.records[1]
	plan = orphanReplacementPlan(plan)
	port, _ := config.ParsePortMapping("127.0.0.1:39123:80")
	plan.Services[1].Config.Network = "cp-demo-default"
	plan.Services[1].Config.NetworkAliases = []string{"shared"}
	plan.Services[1].Config.Publish = []config.PortMapping{port}
	fake.reservations[orphan.ID] = Reservation{Network: "cp-demo-default", NetworkAliases: []string{"shared"}, Publish: []config.PortMapping{port}}
	plan.Services[1].Args = append(plan.Services[1].Args[:len(plan.Services[1].Args)-3], "--publish", "127.0.0.1:39123:80", "--", "/bin/sleep", "30")
	checks := 0
	runner.Call = func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] == "inspect" && args[1] == orphan.ID {
			return json.Marshal(container.Inspection{ID: orphan.ID, Generation: orphan.Generation, Config: container.InspectionConfig{Publish: []config.PortMapping{port}}})
		}
		if args[0] == "__project-preflight" {
			checks++
			if slices.Contains(args, "--publish") != (checks == 2) {
				t.Fatalf("host checks did not exclude only the old endpoint before removal: %v", args)
			}
		}
		return fake.call(ctx, args...)
	}
	if _, err := runner.Up(context.Background(), plan, UpOptions{RemoveOrphans: true, Timeout: time.Second}); err != nil {
		t.Fatal(err)
	}
	if checks != 2 || !reflect.DeepEqual(fake.mutations, []string{"replace " + orphan.ID, "run cp-demo-worker"}) {
		t.Fatalf("checks=%d mutations=%v", checks, fake.mutations)
	}
}

func TestRemoveOrphansFailureKeepsRemovedServicesAbsentAndAllowsRetry(t *testing.T) {
	fake, runner, plan := deployedProject(t)
	kept, orphan := fake.records[0], fake.records[1]
	plan = orphanReplacementPlan(plan)
	fake.failRun = "cp-demo-worker"
	resources := cloneResources(fake.resources)
	if _, err := runner.Up(context.Background(), plan, UpOptions{RemoveOrphans: true, Timeout: time.Second}); err == nil {
		t.Fatal("expected startup failure")
	}
	if len(fake.records) != 1 || fake.records[0].ID != kept.ID || !reflect.DeepEqual(fake.resources, resources) {
		t.Fatalf("rollback changed retained state: %+v", fake.records)
	}
	for _, record := range fake.records {
		if record.ID == orphan.ID {
			t.Fatal("removed orphan was restored")
		}
	}
	fake.failRun = ""
	if _, err := runner.Up(context.Background(), plan, UpOptions{RemoveOrphans: true, Timeout: time.Second}); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveOrphansOrdersStoppedAndRunningRecordsAndForgetsOnlyTheirHistory(t *testing.T) {
	fake, runner, plan := deployedProject(t)
	orphan := fake.records[1]
	fake.records[1].CreatedAt = time.Unix(2, 0)
	retired := container.Record{ID: strings.Repeat("a", 32), Name: "cp-demo-retired", State: container.StateExited, Generation: 0,
		CreatedAt: time.Unix(1, 0), Labels: map[string]string{LabelName: "demo", LabelService: "retired", LabelSpec: "spec-retired"}}
	fake.records = append(fake.records, retired)
	runner.State.Services = map[string]ServiceReceipt{"api": {ID: orphan.ID, Spec: orphan.Labels[LabelSpec]}, "retired": {ID: retired.ID, Spec: retired.Labels[LabelSpec]}}
	plan.Services = plan.Services[:1]
	if _, err := runner.Up(context.Background(), plan, UpOptions{RemoveOrphans: true}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fake.mutations, []string{"replace " + orphan.ID, "replace " + retired.ID}) || len(runner.State.Services) != 0 {
		t.Fatalf("removals=%v history=%v", fake.mutations, runner.State.Services)
	}
}

func TestRemoveOrphansAndExplicitRecreationShareRemovalPreflight(t *testing.T) {
	fake, runner, plan := deployedProject(t)
	oldDB, oldAPI := fake.records[0], fake.records[1]
	plan.Services = plan.Services[:1]
	if _, err := runner.Up(context.Background(), plan, UpOptions{RemoveOrphans: true, Recreate: []string{"db"}}); err != nil {
		t.Fatal(err)
	}
	if len(fake.records) != 1 || fake.records[0].ID == oldDB.ID || !reflect.DeepEqual(fake.mutations, []string{"replace " + oldAPI.ID, "replace " + oldDB.ID, "run cp-demo-db"}) {
		t.Fatalf("recreation and removal: %v %+v", fake.mutations, fake.records)
	}
}

func TestRemoveOrphansBoundsTheExistingRemovalSelection(t *testing.T) {
	fake, runner, plan := deployedProject(t)
	plan.Services = plan.Services[:1]
	for i := 0; i < 64; i++ {
		key := fmt.Sprintf("old-%d", i)
		fake.records = append(fake.records, container.Record{ID: fmt.Sprintf("%032x", i+10), Name: "cp-demo-" + key, State: container.StateExited,
			Labels: map[string]string{LabelName: "demo", LabelService: key, LabelSpec: "old"}})
	}
	if _, err := runner.Up(context.Background(), plan, UpOptions{RemoveOrphans: true}); err == nil || !strings.Contains(err.Error(), "exceeds 64") || len(fake.mutations) != 0 {
		t.Fatalf("unbounded selection: %v %v", err, fake.mutations)
	}
}
