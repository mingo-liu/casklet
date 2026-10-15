package project

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mingo-liu/casklet/internal/container"
)

type fakeProject struct {
	records   []container.Record
	resources map[string]Resource
	mutations []string
	waits     []string
	failRun   string
	failWait  bool
	saves     int
	nextID    int
}

func newFakeProject() *fakeProject { return &fakeProject{resources: map[string]Resource{}} }
func (fake *fakeProject) call(ctx context.Context, args ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	encode := func(value any) ([]byte, error) { return json.Marshal(value) }
	switch args[0] {
	case "ps":
		return encode(fake.records)
	case "network", "volume":
		var values []Resource
		for _, value := range fake.resources {
			if value.Kind == args[0] {
				values = append(values, value)
			}
		}
		return encode(values)
	case "__project-resource":
		key := args[2] + ":" + args[3]
		fake.mutations = append(fake.mutations, strings.Join(args[:4], " "))
		if args[1] == "create" {
			if _, exists := fake.resources[key]; exists {
				return nil, errors.New("already exists")
			}
			receipt := Resource{Kind: args[2], Name: args[3], CreatedAt: time.Unix(int64(100+len(fake.mutations)), 0).UTC()}
			fake.resources[key] = receipt
			return encode(receipt)
		}
		resource, ok := fake.resources[key]
		if ok && resource.CreatedAt.Format(time.RFC3339Nano) != args[4] {
			return nil, errors.New("identity changed")
		}
		delete(fake.resources, key)
		return nil, nil
	case "__project-preflight", "__project-start-preflight":
		return nil, nil
	case "__project-replace":
		for i, record := range fake.records {
			if record.ID == args[1] {
				if fmt.Sprint(record.Generation) != args[2] || record.Labels[LabelName] != args[3] || record.Labels[LabelService] != args[4] || record.Labels[LabelSpec] != args[5] {
					return nil, errors.New("replacement identity changed")
				}
				fake.mutations = append(fake.mutations, "replace "+record.ID)
				fake.records = append(fake.records[:i], fake.records[i+1:]...)
				return nil, nil
			}
		}
		return nil, errors.New("missing")
	case "run":
		labels := map[string]string{}
		name := ""
		for i := 1; i < len(args); i++ {
			if args[i] == "--" {
				break
			}
			if args[i] == "--name" {
				i++
				name = args[i]
			}
			if args[i] == "--label" {
				i++
				key, value, _ := strings.Cut(args[i], "=")
				labels[key] = value
			}
		}
		fake.nextID++
		id := fmt.Sprintf("%032x", fake.nextID)
		fake.records = append(fake.records, container.Record{ID: id, Name: name, State: container.StateRunning, Generation: 1, Labels: labels})
		fake.mutations = append(fake.mutations, "run "+name)
		if fake.failRun == name {
			fake.records[len(fake.records)-1].State = container.StateFailed
			return nil, errors.New("startup failed after publishing record")
		}
		return []byte(id + "\n"), nil
	case "wait":
		fake.waits = append(fake.waits, args[len(args)-1])
		if fake.failWait {
			return nil, errors.New("readiness deadline")
		}
		return []byte("0\n"), nil
	case "inspect":
		for _, record := range fake.records {
			if record.ID == args[1] {
				return encode(container.Inspection{ID: record.ID, State: record.State, Generation: record.Generation, Health: record.Health})
			}
		}
		return nil, errors.New("missing")
	case "start", "stop", "rm":
		id := args[len(args)-1]
		fake.mutations = append(fake.mutations, args[0]+" "+id)
		for i, record := range fake.records {
			if record.ID == id {
				switch args[0] {
				case "start":
					fake.records[i].State = container.StateRunning
				case "stop":
					fake.records[i].State = container.StateExited
				case "rm":
					fake.records = append(fake.records[:i], fake.records[i+1:]...)
				}
				return []byte(id), nil
			}
		}
		return nil, errors.New("missing")
	}
	return nil, fmt.Errorf("unexpected call %v", args)
}

func fakePlan() Plan {
	makeService := func(key string) Service {
		name := "cp-demo-" + key
		fingerprint := "spec-" + key
		return Service{Key: key, Name: name, Fingerprint: fingerprint, Args: []string{"run", "--name", name, "--label", LabelName + "=demo", "--label", LabelService + "=" + key, "--label", LabelSpec + "=" + fingerprint, "--", "/bin/sleep", "30"}}
	}
	db := makeService("db")
	api := makeService("api")
	api.DependsOn = map[string]string{"db": "healthy"}
	return Plan{Name: "demo", Networks: []string{"cp-demo-default"}, Volumes: []string{"cp-demo-data"}, Services: []Service{db, api}}
}
func fakeRunner(fake *fakeProject) *Runner {
	return &Runner{Call: fake.call, State: State{Version: 1, Name: "demo", Resources: map[string]Resource{}}, Save: func(State) error { fake.saves++; return nil }}
}

func TestProjectRepeatableUpAndDownPreserveDataVolumes(t *testing.T) {
	fake := newFakeProject()
	runner := fakeRunner(fake)
	plan := fakePlan()
	first, err := runner.Up(context.Background(), plan, UpOptions{Timeout: time.Second})
	if err != nil || len(first) != 2 || len(fake.waits) != 1 {
		t.Fatalf("up %v %v", first, err)
	}
	ids := []string{first[0].ID, first[1].ID}
	mutations := len(fake.mutations)
	second, err := runner.Up(context.Background(), plan, UpOptions{Timeout: time.Second})
	if err != nil || !reflect.DeepEqual(ids, []string{second[0].ID, second[1].ID}) || len(fake.mutations) != mutations {
		t.Fatalf("repeat up %v %v", second, err)
	}
	if err := runner.Down(context.Background(), "demo", false); err != nil {
		t.Fatal(err)
	}
	if len(fake.records) != 0 || len(fake.resources) != 1 || fake.resources["volume:cp-demo-data"].Name == "" {
		t.Fatalf("data not preserved %v %v", fake.records, fake.resources)
	}
	if _, err := runner.Up(context.Background(), plan, UpOptions{Timeout: time.Second}); err != nil {
		t.Fatal(err)
	}
	if err := runner.Down(context.Background(), "demo", true); err != nil || len(fake.resources) != 0 {
		t.Fatalf("explicit volume removal %v %v", fake.resources, err)
	}
}

func TestProjectRejectsDriftAndUnownedResourcesBeforeMutation(t *testing.T) {
	for _, kind := range []string{"container", "spec", "resource", "extra-service"} {
		t.Run(kind, func(t *testing.T) {
			fake := newFakeProject()
			runner := fakeRunner(fake)
			plan := fakePlan()
			switch kind {
			case "container":
				fake.records = []container.Record{{ID: "manual", Name: plan.Services[0].Name}}
			case "spec":
				fake.records = []container.Record{{ID: "old", Name: plan.Services[0].Name, Labels: map[string]string{LabelName: "demo", LabelService: "db", LabelSpec: "other"}}}
			case "resource":
				fake.resources["volume:cp-demo-data"] = Resource{Kind: "volume", Name: "cp-demo-data", CreatedAt: time.Now()}
			case "extra-service":
				fake.records = []container.Record{{ID: "old", Name: "cp-demo-extra", Labels: map[string]string{LabelName: "demo", LabelService: "extra", LabelSpec: "old"}}}
			}
			if _, err := runner.Up(context.Background(), plan, UpOptions{Timeout: time.Second}); err == nil || len(fake.mutations) != 0 {
				t.Fatalf("drift mutated resources: %v %v", err, fake.mutations)
			}
		})
	}
}

func TestProjectFailureCleansOnlyCurrentTransaction(t *testing.T) {
	for _, failure := range []string{"run", "healthy"} {
		t.Run(failure, func(t *testing.T) {
			fake := newFakeProject()
			runner := fakeRunner(fake)
			plan := fakePlan()
			if failure == "run" {
				fake.failRun = "cp-demo-api"
			} else {
				fake.failWait = true
			}
			unrelated := container.Record{ID: "manual", Name: "manual", State: container.StateRunning}
			fake.records = append(fake.records, unrelated)
			if _, err := runner.Up(context.Background(), plan, UpOptions{Timeout: time.Millisecond}); err == nil {
				t.Fatal("expected failure")
			}
			if !reflect.DeepEqual(fake.records, []container.Record{unrelated}) || len(fake.resources) != 0 || len(runner.State.Resources) != 0 {
				t.Fatalf("rollback leaked or removed unrelated state: %v %v", fake.records, fake.resources)
			}
		})
	}
}

func TestProjectFailurePreservesReusedServicesAndResources(t *testing.T) {
	fake := newFakeProject()
	runner := fakeRunner(fake)
	plan := fakePlan()
	initial := plan
	initial.Services = initial.Services[:1]
	if _, err := runner.Up(context.Background(), initial, UpOptions{Timeout: time.Second}); err != nil {
		t.Fatal(err)
	}
	original := fake.records[0].ID
	fake.failRun = "cp-demo-api"
	if _, err := runner.Up(context.Background(), plan, UpOptions{Timeout: time.Second}); err == nil {
		t.Fatal("expected failed service")
	}
	if len(fake.records) != 1 || fake.records[0].ID != original || fake.records[0].State != container.StateRunning || len(fake.resources) != 2 {
		t.Fatalf("reused state changed: %v %v", fake.records, fake.resources)
	}
}

func TestProjectDownPreservesReplacedResources(t *testing.T) {
	fake := newFakeProject()
	runner := fakeRunner(fake)
	original := Resource{Kind: "volume", Name: "cp-demo-data", CreatedAt: time.Unix(100, 0)}
	runner.State.Resources["volume:cp-demo-data"] = original
	replacement := original
	replacement.CreatedAt = time.Unix(200, 0)
	fake.resources["volume:cp-demo-data"] = replacement
	if err := runner.Down(context.Background(), "demo", true); err == nil || !sameResource(fake.resources["volume:cp-demo-data"], replacement) {
		t.Fatalf("replacement removed: %v", err)
	}
}

func TestProjectSaveFailureRollsBackNewResources(t *testing.T) {
	fake := newFakeProject()
	runner := fakeRunner(fake)
	runner.Save = func(State) error { return errors.New("disk full") }
	if _, err := runner.Up(context.Background(), fakePlan(), UpOptions{Timeout: time.Second}); err == nil || len(fake.resources) != 0 {
		t.Fatalf("save failure cleanup: %v %v", err, fake.resources)
	}
}

func TestProjectCancelledWaitUsesIndependentCleanupContext(t *testing.T) {
	fake := newFakeProject()
	runner := fakeRunner(fake)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner.Call = func(operationCtx context.Context, args ...string) ([]byte, error) {
		if args[0] == "wait" {
			cancel()
			return nil, ctx.Err()
		}
		return fake.call(operationCtx, args...)
	}
	if _, err := runner.Up(ctx, fakePlan(), UpOptions{Timeout: time.Second}); !errors.Is(err, context.Canceled) || len(fake.records) != 0 || len(fake.resources) != 0 {
		t.Fatalf("cancel cleanup %v %v %v", err, fake.records, fake.resources)
	}
}

func TestProjectIgnoresUnrelatedVolumeNamingStyles(t *testing.T) {
	fake := newFakeProject()
	runner := fakeRunner(fake)
	unrelated := Resource{Kind: "volume", Name: "Other_Data", CreatedAt: time.Now()}
	fake.resources["volume:"+unrelated.Name] = unrelated
	if _, err := runner.Up(context.Background(), fakePlan(), UpOptions{Timeout: time.Second}); err != nil {
		t.Fatal(err)
	}
	if err := runner.Down(context.Background(), "demo", true); err != nil || !sameResource(fake.resources["volume:"+unrelated.Name], unrelated) {
		t.Fatalf("unrelated volume %v", err)
	}
}

func TestProjectDownContinuesAfterPartialFailure(t *testing.T) {
	fake := newFakeProject()
	runner := fakeRunner(fake)
	if _, err := runner.Up(context.Background(), fakePlan(), UpOptions{Timeout: time.Second}); err != nil {
		t.Fatal(err)
	}
	retained := fake.records[0].ID
	runner.Call = func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] == "stop" && args[len(args)-1] == retained {
			return nil, errors.New("stop failed")
		}
		if args[0] == "__project-resource" && args[1] == "rm" && len(fake.records) > 0 {
			return nil, errors.New("still referenced")
		}
		return fake.call(ctx, args...)
	}
	if err := runner.Down(context.Background(), "demo", true); err == nil || len(fake.records) != 1 || fake.records[0].ID != retained || len(runner.State.Resources) != 2 {
		t.Fatalf("partial failure lost receipts %v %v", err, fake.records)
	}
	runner.Call = fake.call
	if err := runner.Down(context.Background(), "demo", true); err != nil || len(fake.records) != 0 || len(fake.resources) != 0 {
		t.Fatalf("retry failed %v", err)
	}
}

func TestExternalVolumeRestoreReuseAndOwnershipRelease(t *testing.T) {
	for _, receipt := range []string{"migrated", "stale-restored", "owned"} {
		t.Run(receipt, func(t *testing.T) {
			fake := newFakeProject()
			runner := fakeRunner(fake)
			plan := fakePlan()
			plan.Volumes = nil
			plan.ExternalVolumes = []string{"cp-demo-data"}
			external := Resource{Kind: "volume", Name: "cp-demo-data", CreatedAt: time.Unix(200, 0)}
			fake.resources["volume:"+external.Name] = external
			if receipt != "migrated" {
				owned := external
				if receipt == "stale-restored" {
					owned.CreatedAt = time.Unix(100, 0)
				}
				runner.State.Resources["volume:"+external.Name] = owned
			}
			var saved State
			runner.Save = func(state State) error {
				saved = State{Version: state.Version, Name: state.Name, Resources: map[string]Resource{}}
				for key, resource := range state.Resources {
					saved.Resources[key] = resource
				}
				return nil
			}
			first, err := runner.Up(context.Background(), plan, UpOptions{Timeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			if _, owned := runner.State.Resources["volume:"+external.Name]; owned {
				t.Fatal("external volume acquired ownership")
			}
			if _, owned := saved.Resources["volume:"+external.Name]; owned {
				t.Fatal("ownership release not persisted")
			}
			mutations := len(fake.mutations)
			second, err := runner.Up(context.Background(), plan, UpOptions{Timeout: time.Second})
			if err != nil || second[0].ID != first[0].ID || len(fake.mutations) != mutations {
				t.Fatalf("external reuse: %v %v", second, err)
			}
			// Down receives no config: reload only persisted ownership receipts.
			runner.State = saved
			if err := runner.Down(context.Background(), "demo", true); err != nil {
				t.Fatal(err)
			}
			if len(fake.resources) != 1 || !sameResource(fake.resources["volume:"+external.Name], external) {
				t.Fatalf("external removed: %v", fake.resources)
			}
		})
	}
}

func TestExternalVolumePreflightAndSaveFailurePreventMutations(t *testing.T) {
	for _, failure := range []string{"missing", "save", "unowned-network", "service-drift"} {
		t.Run(failure, func(t *testing.T) {
			fake := newFakeProject()
			runner := fakeRunner(fake)
			plan := fakePlan()
			plan.Volumes = nil
			plan.ExternalVolumes = []string{"cp-demo-data"}
			external := Resource{Kind: "volume", Name: "cp-demo-data", CreatedAt: time.Unix(200, 0)}
			runner.State.Resources["volume:"+external.Name] = external
			if failure != "missing" {
				fake.resources["volume:"+external.Name] = external
			}
			switch failure {
			case "save":
				runner.Save = func(State) error { return errors.New("disk full") }
			case "unowned-network":
				fake.resources["network:cp-demo-default"] = Resource{Kind: "network", Name: "cp-demo-default", CreatedAt: time.Unix(200, 0)}
			case "service-drift":
				fake.records = []container.Record{{Name: plan.Services[0].Name}}
			}
			if _, err := runner.Up(context.Background(), plan, UpOptions{Timeout: time.Second}); err == nil || len(fake.mutations) != 0 {
				t.Fatalf("unsafe mutation: %v %v", err, fake.mutations)
			}
			if _, owned := runner.State.Resources["volume:"+external.Name]; !owned {
				t.Fatal("failed preflight/save relinquished receipt")
			}
		})
	}
}

func TestExternalVolumeSurvivesRollbackAfterOwnershipRelease(t *testing.T) {
	fake := newFakeProject()
	runner := fakeRunner(fake)
	plan := fakePlan()
	plan.ExternalVolumes = []string{"cp-demo-restored"}
	external := Resource{Kind: "volume", Name: "cp-demo-restored", CreatedAt: time.Unix(200, 0)}
	fake.resources["volume:"+external.Name] = external
	runner.State.Resources["volume:"+external.Name] = external
	fake.failRun = "cp-demo-api"
	if _, err := runner.Up(context.Background(), plan, UpOptions{Timeout: time.Second}); err == nil {
		t.Fatal("expected failure")
	}
	if len(fake.records) != 0 || len(fake.resources) != 1 || !sameResource(fake.resources["volume:"+external.Name], external) || len(runner.State.Resources) != 0 {
		t.Fatalf("rollback changed external data: %v %v %v", fake.records, fake.resources, runner.State)
	}
	if err := runner.Down(context.Background(), "demo", true); err != nil || len(fake.resources) != 1 {
		t.Fatalf("down after rollback: %v", err)
	}
}

func TestManagedVolumeConversionToExternalPreservesRunningServices(t *testing.T) {
	fake := newFakeProject()
	runner := fakeRunner(fake)
	plan := fakePlan()
	first, err := runner.Up(context.Background(), plan, UpOptions{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	external := fake.resources["volume:cp-demo-data"]
	plan.Volumes = nil
	plan.ExternalVolumes = []string{external.Name}
	mutations, saves := len(fake.mutations), fake.saves
	second, err := runner.Up(context.Background(), plan, UpOptions{Timeout: time.Second})
	if err != nil || len(second) != len(first) || second[0].ID != first[0].ID || second[1].ID != first[1].ID || len(fake.mutations) != mutations || fake.saves != saves+1 {
		t.Fatalf("conversion changed running deployment: %+v %v %v", second, err, fake.mutations)
	}
	if err := runner.Down(context.Background(), "demo", true); err != nil || len(fake.resources) != 1 || !sameResource(fake.resources["volume:"+external.Name], external) {
		t.Fatalf("converted volume removed: %v %v", err, fake.resources)
	}
}
