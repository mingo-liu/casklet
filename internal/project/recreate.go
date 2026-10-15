package project

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mingo-liu/casklet/internal/config"
	"github.com/mingo-liu/casklet/internal/container"
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
	for _, record := range all {
		if removed[record.ID] {
			continue
		}
		data, err := runner.Call(ctx, "inspect", record.ID)
		if err != nil {
			return err
		}
		var saved container.Inspection
		if json.Unmarshal(data, &saved) != nil || saved.ID != record.ID || saved.Generation != record.Generation {
			return fmt.Errorf("container %q changed during project preflight", record.Name)
		}
		for _, service := range plan.Services {
			if current, found := existing[service.Key]; found && !selected[service.Key] && (current.ID == record.ID || current.State == container.StateRunning || current.State == container.StateStarting) {
				// Unchanged running services keep their actual saved configuration.
				continue
			}
			if config.IsNamedNetwork(service.Config.Network) && saved.Config.Network == service.Config.Network {
				for _, wanted := range append([]string{service.Name}, service.Config.NetworkAliases...) {
					for _, occupied := range append([]string{record.Name}, saved.Config.NetworkAliases...) {
						if wanted == occupied {
							return fmt.Errorf("service %q network alias %q is reserved by container %q", service.Key, wanted, record.Name)
						}
					}
				}
			}
			if record.State == container.StateRunning || record.State == container.StateStarting {
				for _, wanted := range service.Config.Publish {
					for _, occupied := range saved.Config.Publish {
						if config.PortsConflict(wanted, occupied) {
							return fmt.Errorf("service %q host port conflicts with container %q", service.Key, record.Name)
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
	for i := 1; i < len(args); i++ {
		if args[i] == "--" {
			return append(result, args[i:]...)
		}
		name, value, inline := strings.Cut(strings.TrimLeft(args[i], "-"), "=")
		if name == "publish" || name == "p" {
			end := i
			if !inline {
				end++
				value = args[end]
			}
			port, err := config.ParsePortMapping(value)
			exempt := false
			if err == nil {
				for _, previous := range oldPorts {
					if port.HostIP == previous.HostIP && port.HostPort == previous.HostPort && port.Protocol == previous.Protocol {
						exempt = true
					}
				}
			}
			if !exempt {
				result = append(result, args[i:end+1]...)
			}
			i = end
			continue
		}
		result = append(result, args[i])
	}
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
