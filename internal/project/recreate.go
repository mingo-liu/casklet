package project

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mingo-liu/casklet/internal/config"
	"github.com/mingo-liu/casklet/internal/container"
	"github.com/mingo-liu/casklet/internal/runargs"
)

// Select validates logical keys before inventory or lifecycle operations.
func Select(plan Plan, names []string) (map[string]bool, error) {
	desired := map[string]bool{}
	for _, service := range plan.Services {
		desired[service.Key] = true
	}
	selected := map[string]bool{}
	for _, name := range names {
		if !desired[name] {
			return nil, fmt.Errorf("unknown project service %q for --recreate", name)
		}
		if selected[name] {
			return nil, fmt.Errorf("duplicate --recreate service %q", name)
		}
		selected[name] = true
	}
	return selected, nil
}

// Saved configurations, rather than desired hashes, reserve ports and aliases.
// Selected old records are removed together before any replacement starts.
func (runner *Runner) checkConflicts(ctx context.Context, plan Plan, all []container.Record, existing map[string]container.Record, selected map[string]bool) error {
	removed := map[string]bool{}
	for key, record := range existing {
		if selected[key] {
			removed[record.ID] = true
		}
	}
	reservations, err := runner.reservations(ctx, all, removed)
	if err != nil {
		return err
	}
	for _, saved := range reservations {
		for _, service := range plan.Services {
			if current, found := existing[service.Key]; found && !selected[service.Key] && (current.ID == saved.ID || current.State == container.StateRunning || current.State == container.StateStarting) {
				// Unchanged running services keep their actual saved configuration.
				continue
			}
			if config.IsNamedNetwork(service.Config.Network) && saved.Network == service.Config.Network {
				for _, wanted := range append([]string{service.Name}, service.Config.NetworkAliases...) {
					for _, occupied := range append([]string{saved.Name}, saved.NetworkAliases...) {
						if wanted == occupied {
							return fmt.Errorf("service %q network alias %q is reserved by container %q", service.Key, wanted, saved.Name)
						}
					}
				}
			}
			if saved.State == container.StateRunning || saved.State == container.StateStarting {
				for _, wanted := range service.Config.Publish {
					for _, occupied := range saved.Publish {
						if config.PortsConflict(wanted, occupied) {
							return fmt.Errorf("service %q host port conflicts with container %q", service.Key, saved.Name)
						}
					}
				}
			}
		}
	}
	return nil
}

func (runner *Runner) selectedPorts(ctx context.Context, existing map[string]container.Record, selected map[string]bool) ([]config.PortMapping, error) {
	var ports []config.PortMapping
	for key, record := range existing {
		if !selected[key] || record.State != container.StateRunning && record.State != container.StateStarting {
			continue
		}
		data, err := runner.Call(ctx, "inspect", record.ID)
		if err != nil {
			return nil, err
		}
		var saved container.Inspection
		if json.Unmarshal(data, &saved) != nil || saved.ID != record.ID || saved.Generation != record.Generation {
			return nil, fmt.Errorf("service %q changed during preflight", key)
		}
		ports = append(ports, saved.Config.Publish...)
	}
	return ports, nil
}

// The first source check excludes identical host endpoints held by selected
// running records. Changed scopes are checked conservatively before removal.
// Every desired endpoint is checked in full after removal, before startup.
func preflightArguments(args []string, oldPorts []config.PortMapping) []string {
	result := []string{"__project-preflight"}
	consumed := 1
	_ = runargs.Walk(args, func(name, value string, index, end int) error {
		consumed = end + 1
		exempt := false
		if name == "publish" || name == "p" {
			port, err := config.ParsePortMapping(value)
			if err == nil {
				for _, previous := range oldPorts {
					if port.HostIP == previous.HostIP && port.HostPort == previous.HostPort && port.Protocol == previous.Protocol {
						exempt = true
					}
				}
			}
		}
		if !exempt {
			result = append(result, args[index:end+1]...)
		}
		return nil
	})
	result = append(result, args[consumed:]...)
	return result
}

func (runner *Runner) retainedPreflightArguments(ctx context.Context, record container.Record, name, key string) ([]string, error) {
	data, err := runner.Call(ctx, "inspect", record.ID)
	if err != nil {
		return nil, err
	}
	var saved container.Inspection
	if json.Unmarshal(data, &saved) != nil || saved.ID != record.ID || saved.Generation != record.Generation {
		return nil, fmt.Errorf("service %q changed during retained-source preflight", key)
	}
	args := []string{"__project-start-preflight", record.ID, fmt.Sprint(record.Generation), name, key, record.Labels[LabelSpec]}
	for _, port := range saved.Config.Publish {
		args = append(args, fmt.Sprintf("%s:%d:%d/%s", port.HostIP, port.HostPort, port.ContainerPort, port.Protocol))
	}
	return args, nil
}
