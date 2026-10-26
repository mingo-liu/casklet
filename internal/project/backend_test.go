package project

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/mingo-liu/casklet/internal/container"
)

// Domain tests can supply values without implementing a CLI or JSON protocol.
type valueBackend struct {
	records   []container.Record
	resources map[string]Resource
	status    []ServiceStatus
	sampled   []string
}

func (backend *valueBackend) Records(context.Context, string) ([]container.Record, error) {
	return backend.records, nil
}
func (backend *valueBackend) Status(_ context.Context, _ string, ids []string) ([]ServiceStatus, error) {
	backend.sampled = append([]string(nil), ids...)
	return backend.status, nil
}
func (backend *valueBackend) Resources(_ context.Context, kind string) ([]Resource, error) {
	result := []Resource{}
	for _, resource := range backend.resources {
		if resource.Kind == kind {
			result = append(result, resource)
		}
	}
	return result, nil
}
func (backend *valueBackend) CreateResource(_ context.Context, kind, name string) (Resource, error) {
	resource := Resource{Kind: kind, Name: name, CreatedAt: time.Unix(100, 0).UTC()}
	backend.resources[kind+":"+name] = resource
	return resource, nil
}
func (backend *valueBackend) RemoveResource(_ context.Context, resource Resource) error {
	delete(backend.resources, resource.Kind+":"+resource.Name)
	return nil
}

func TestRunnerUsesTypedInventoryReadinessAndResources(t *testing.T) {
	plan := fakePlan()
	plan.Services = plan.Services[:1]
	service := plan.Services[0]
	record := container.Record{ID: "00000000000000000000000000000001", Name: service.Name, State: container.StateRunning,
		Labels: map[string]string{LabelName: plan.Name, LabelService: service.Key, LabelSpec: service.Fingerprint}}
	backend := &valueBackend{records: []container.Record{record}, resources: map[string]Resource{},
		status: []ServiceStatus{{ID: record.ID, Project: plan.Name, Service: service.Key, State: container.StateRunning}}}
	runner := Runner{Backend: backend, State: State{Version: 1, Name: plan.Name, Resources: map[string]Resource{}}, Save: func(State) error { return nil },
		Call: func(context.Context, ...string) ([]byte, error) {
			t.Fatal("typed operations escaped into command transport")
			return nil, nil
		}}
	got, err := runner.Up(context.Background(), plan, UpOptions{})
	if err != nil || len(got) != 1 || got[0].ID != record.ID || len(backend.resources) != 2 {
		t.Fatalf("up=%v resources=%v: %v", got, backend.resources, err)
	}
	executions := map[string]serviceExecution{service.Key: {ID: record.ID}}
	if err := runner.waitServices(context.Background(), plan, executions); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(backend.sampled, []string{record.ID}) {
		t.Fatal("readiness lost pinned IDs", backend.sampled)
	}
	for _, resource := range backend.resources {
		if err := runner.removeResource(context.Background(), resource); err != nil {
			t.Fatal(err)
		}
	}
	if len(backend.resources) != 0 || len(runner.State.Resources) != 0 {
		t.Fatal("typed resource removal lost receipts")
	}
}

func TestCommandBackendRejectsMalformedResponsesAndPreservesErrors(t *testing.T) {
	for _, operation := range []string{"records", "status", "resources", "create"} {
		for _, broken := range []bool{false, true} {
			sentinel := errors.New("transport failure")
			backend := CommandBackend{Call: func(context.Context, ...string) ([]byte, error) {
				if broken {
					return nil, sentinel
				}
				return []byte("{"), nil
			}}
			var err error
			switch operation {
			case "records":
				_, err = backend.Records(context.Background(), "demo")
			case "status":
				_, err = backend.Status(context.Background(), "demo", []string{"pinned"})
			case "resources":
				_, err = backend.Resources(context.Background(), "volume")
			case "create":
				_, err = backend.CreateResource(context.Background(), "volume", "cp-demo-data")
			}
			if err == nil || broken && !errors.Is(err, sentinel) {
				t.Fatalf("%s broken=%v: %v", operation, broken, err)
			}
		}
	}
}
