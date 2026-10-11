package project

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mingo-liu/casklet/internal/container"
)

// Call executes the existing CLI operations, preserving platform authentication,
// path translation, host-port checks and guest lifecycle fencing.
type Call func(context.Context, ...string) ([]byte, error)

type Runner struct {
	Call  Call
	Save  func(State) error
	State State
}

func (runner *Runner) Records(ctx context.Context) ([]container.Record, error) {
	data, err := runner.Call(ctx, "ps", "-a", "--json")
	if err != nil {
		return nil, err
	}
	var records []container.Record
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, errors.New("invalid container list response")
	}
	return records, nil
}

func Managed(records []container.Record, name string) []container.Record {
	result := []container.Record{}
	for _, record := range records {
		if record.Labels[LabelName] == name && record.Labels[LabelService] != "" && record.Labels[LabelSpec] != "" {
			result = append(result, record)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func (runner *Runner) resources(ctx context.Context, kind string) (map[string]Resource, error) {
	data, err := runner.Call(ctx, kind, "ls", "--json")
	if err != nil {
		return nil, err
	}
	var values []Resource
	if err := json.Unmarshal(data, &values); err != nil {
		return nil, fmt.Errorf("invalid %s list response", kind)
	}
	result := map[string]Resource{}
	for _, value := range values {
		value.Kind = kind
		if value.Name == "" || value.CreatedAt.IsZero() {
			return nil, errors.New("invalid resource identity")
		}
		result[value.Name] = value
	}
	return result, nil
}

func sameResource(a, b Resource) bool {
	return a.Kind == b.Kind && a.Name == b.Name && a.CreatedAt.Equal(b.CreatedAt)
}

func (runner *Runner) Up(ctx context.Context, plan Plan, timeout time.Duration) (records []container.Record, err error) {
	all, err := runner.Records(ctx)
	if err != nil {
		return nil, err
	}
	existing := map[string]container.Record{}
	desired := map[string]bool{}
	for _, service := range plan.Services {
		desired[service.Key] = true
		for _, record := range all {
			if record.Name == service.Name {
				if record.Labels[LabelName] != plan.Name || record.Labels[LabelService] != service.Key || record.Labels[LabelSpec] != service.Fingerprint {
					return nil, fmt.Errorf("service %q already exists with different configuration or ownership; use project down before recreating", service.Key)
				}
				existing[service.Key] = record
			}
		}
	}
	for _, record := range Managed(all, plan.Name) {
		if !desired[record.Labels[LabelService]] {
			return nil, errors.New("project has services absent from this config; use project down before recreating")
		}
	}
	inventories := map[string]map[string]Resource{}
	for _, kind := range []string{"network", "volume"} {
		inventory, e := runner.resources(ctx, kind)
		if e != nil {
			return nil, e
		}
		inventories[kind] = inventory
	}
	var needed []Resource
	for _, entry := range []struct {
		kind  string
		names []string
	}{{"network", plan.Networks}, {"volume", plan.Volumes}} {
		for _, name := range entry.names {
			key := entry.kind + ":" + name
			if resource, found := inventories[entry.kind][name]; found {
				owned, ok := runner.State.Resources[key]
				if !ok || !sameResource(owned, resource) {
					return nil, fmt.Errorf("%s %q exists without matching project ownership", entry.kind, name)
				}
			} else {
				needed = append(needed, Resource{Kind: entry.kind, Name: name})
			}
		}
	}
	newKeys := 0
	for _, resource := range needed {
		if _, found := runner.State.Resources[resource.Kind+":"+resource.Name]; !found {
			newKeys++
		}
	}
	if len(runner.State.Resources)+newKeys > 64 {
		return nil, errors.New("project resource history is full; use project down before recreating")
	}
	var tokenBytes [16]byte
	if _, err := rand.Read(tokenBytes[:]); err != nil {
		return nil, err
	}
	token := hex.EncodeToString(tokenBytes[:])
	var created []Resource
	defer func() {
		if err != nil {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			err = errors.Join(err, runner.rollback(cleanupCtx, plan.Name, token, created))
		}
	}()
	for _, resource := range needed {
		data, e := runner.Call(ctx, "__project-resource", "create", resource.Kind, resource.Name)
		if e != nil {
			return nil, e
		}
		var receipt Resource
		if json.Unmarshal(data, &receipt) != nil || receipt.Kind != resource.Kind || receipt.Name != resource.Name || receipt.CreatedAt.IsZero() {
			return nil, errors.New("invalid project creation receipt; preserving unverified resource")
		}
		created = append(created, receipt)
		runner.State.Resources[resource.Kind+":"+resource.Name] = receipt
		if err = runner.Save(runner.State); err != nil {
			return nil, err
		}
	}

	ids := map[string]string{}
	for _, service := range plan.Services {
		dependencies := make([]string, 0, len(service.DependsOn))
		for dep := range service.DependsOn {
			dependencies = append(dependencies, dep)
		}
		sort.Strings(dependencies)
		for _, dep := range dependencies {
			if service.DependsOn[dep] == "healthy" {
				if _, err = runner.Call(ctx, "wait", "--healthy", "--timeout", timeout.String(), ids[dep]); err != nil {
					return nil, fmt.Errorf("service %q dependency %q did not become healthy: %w", service.Key, dep, err)
				}
			} else {
				data, e := runner.Call(ctx, "inspect", ids[dep])
				if e != nil {
					return nil, e
				}
				var inspection container.Inspection
				if json.Unmarshal(data, &inspection) != nil || inspection.ID != ids[dep] || inspection.State != container.StateRunning {
					return nil, fmt.Errorf("service %q dependency %q is not running", service.Key, dep)
				}
			}
		}
		if record, ok := existing[service.Key]; ok {
			ids[service.Key] = record.ID
			if record.State != container.StateRunning {
				if _, err = runner.Call(ctx, "start", record.ID); err != nil {
					return nil, err
				}
			}
		} else {
			args := append([]string{"run", "--label", LabelTransaction + "=" + token}, service.Args[1:]...)
			data, e := runner.Call(ctx, args...)
			if e != nil {
				return nil, e
			}
			id := strings.TrimSpace(string(data))
			if len(id) != 32 {
				return nil, errors.New("invalid new container identity")
			}
			if _, e := hex.DecodeString(id); e != nil {
				return nil, errors.New("invalid new container identity")
			}
			ids[service.Key] = id
		}
	}
	all, err = runner.Records(ctx)
	if err != nil {
		return nil, err
	}
	records = Managed(all, plan.Name)
	for _, record := range records {
		if ids[record.Labels[LabelService]] != record.ID {
			return nil, errors.New("project service identity changed during startup")
		}
	}
	if len(records) != len(plan.Services) {
		return nil, errors.New("project services changed during startup")
	}
	return records, nil
}

func (runner *Runner) rollback(ctx context.Context, name, token string, created []Resource) error {
	records, err := runner.Records(ctx)
	var failures []error
	if err != nil {
		failures = append(failures, err)
	} else {
		for i := len(records) - 1; i >= 0; i-- {
			record := records[i]
			if record.Labels[LabelName] != name || record.Labels[LabelTransaction] != token {
				continue
			}
			if _, err := runner.Call(ctx, "stop", "--timeout", "0s", record.ID); err != nil {
				failures = append(failures, err)
				continue
			}
			if _, err := runner.Call(ctx, "rm", record.ID); err != nil {
				failures = append(failures, err)
			}
		}
	}
	for i := len(created) - 1; i >= 0; i-- {
		if err := runner.removeResource(ctx, created[i]); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (runner *Runner) removeResource(ctx context.Context, resource Resource) error {
	values, err := runner.resources(ctx, resource.Kind)
	if err != nil {
		return err
	}
	current, found := values[resource.Name]
	if found {
		if !sameResource(current, resource) {
			return fmt.Errorf("%s %q was replaced; preserving it", resource.Kind, resource.Name)
		}
		if _, err := runner.Call(ctx, "__project-resource", "rm", resource.Kind, resource.Name, resource.CreatedAt.Format(time.RFC3339Nano)); err != nil {
			return err
		}
	}
	delete(runner.State.Resources, resource.Kind+":"+resource.Name)
	return runner.Save(runner.State)
}

func (runner *Runner) Down(ctx context.Context, name string, removeVolumes bool) error {
	records, err := runner.Records(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, record := range Managed(records, name) {
		if _, err := runner.Call(ctx, "stop", record.ID); err != nil {
			failures = append(failures, err)
			continue
		}
		if _, err := runner.Call(ctx, "rm", record.ID); err != nil {
			failures = append(failures, err)
		}
	}
	keys := make([]string, 0, len(runner.State.Resources))
	for key := range runner.State.Resources {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		resource := runner.State.Resources[key]
		if resource.Kind == "volume" && !removeVolumes {
			continue
		}
		if err := runner.removeResource(ctx, resource); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
