package project

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/mingo-liu/casklet/internal/container"
)

// Backend exposes project inventory, readiness and resource operations as values.
// An empty inventory name selects all containers; a name selects project members.
// Lifecycle commands keep their existing transport and cancellation behavior.
type Backend interface {
	Records(ctx context.Context, name string) ([]container.Record, error)
	Status(ctx context.Context, name string, ids []string) ([]ServiceStatus, error)
	Resources(ctx context.Context, kind string) ([]Resource, error)
	CreateResource(ctx context.Context, kind, name string) (Resource, error)
	RemoveResource(ctx context.Context, resource Resource) error
}

// CommandBackend adapts existing command paths without moving their platform
// authentication, resource preflight or lifecycle policies into the orchestrator.
// The host overrides Records to use its bounded private inventory pages.
type CommandBackend struct{ Call Call }

func (backend CommandBackend) Records(ctx context.Context, name string) ([]container.Record, error) {
	args := []string{"ps", "-a", "--json"}
	if name != "" {
		args = append(args, "--filter", "label="+LabelName+"="+name)
	}
	data, err := backend.Call(ctx, args...)
	if err != nil {
		return nil, err
	}
	var records []container.Record
	if json.Unmarshal(data, &records) != nil {
		return nil, errors.New("invalid container list response")
	}
	return records, nil
}

func (backend CommandBackend) Status(ctx context.Context, name string, ids []string) ([]ServiceStatus, error) {
	args := append([]string{"__project-status", name}, ids...)
	data, err := backend.Call(ctx, args...)
	if err != nil {
		return nil, err
	}
	var records []ServiceStatus
	if json.Unmarshal(data, &records) != nil {
		return nil, errors.New("invalid project readiness response")
	}
	return records, nil
}

func (backend CommandBackend) Resources(ctx context.Context, kind string) ([]Resource, error) {
	data, err := backend.Call(ctx, kind, "ls", "--json")
	if err != nil {
		return nil, err
	}
	var resources []Resource
	if json.Unmarshal(data, &resources) != nil {
		return nil, fmt.Errorf("invalid %s list response", kind)
	}
	return resources, nil
}

func (backend CommandBackend) CreateResource(ctx context.Context, kind, name string) (Resource, error) {
	data, err := backend.Call(ctx, "__project-resource", "create", kind, name)
	if err != nil {
		return Resource{}, err
	}
	var resource Resource
	if json.Unmarshal(data, &resource) != nil {
		return resource, errors.New("invalid project creation receipt; preserving unverified resource")
	}
	return resource, nil
}

func (backend CommandBackend) RemoveResource(ctx context.Context, resource Resource) error {
	_, err := backend.Call(ctx, "__project-resource", "rm", resource.Kind, resource.Name, resource.CreatedAt.Format(time.RFC3339Nano))
	return err
}
