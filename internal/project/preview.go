package project

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"reflect"
	"sort"
	"strings"

	"github.com/mingo-liu/casklet/internal/config"
	"github.com/mingo-liu/casklet/internal/container"
)

// ServiceReceipt contains comparison digests, never configuration values.
// ID and Spec bind the baseline to one immutable deployed configuration.
type ServiceReceipt struct {
	ID     string            `json:"id"`
	Spec   string            `json:"spec"`
	Fields map[string]string `json:"fields"`
}

func fieldName(name string) string {
	switch name {
	case "network_aliases":
		return "network-alias"
	case "restart_policy":
		return "restart"
	case "cpu_quota":
		return "cpus"
	case "labels":
		return "label"
	case "mounts":
		return "mount"
	}
	return strings.ReplaceAll(name, "_", "-")
}

// FieldDigests uses the same parsed configuration as the service fingerprint.
// Callers canonicalize environment and alias order before hashing.
func FieldDigests(cfg config.Config, entrypoint *string) map[string]string {
	data, _ := json.Marshal(cfg)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(data, &fields)
	fields["entrypoint"], _ = json.Marshal(entrypoint)
	result := make(map[string]string, len(fields))
	for name, value := range fields {
		digest := sha256.Sum256(value)
		result[fieldName(name)] = hex.EncodeToString(digest[:])
	}
	return result
}

var previewFields = func() map[string]bool {
	fields := map[string]bool{"entrypoint": true}
	typeOf := reflect.TypeFor[config.Config]()
	for i := 0; i < typeOf.NumField(); i++ {
		name, _, _ := strings.Cut(typeOf.Field(i).Tag.Get("json"), ",")
		fields[fieldName(name)] = true
	}
	return fields
}()

func validDigest(value string, length int) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(value) == length && len(decoded)*2 == length && strings.ToLower(value) == value
}

func validateServiceReceipts(services map[string]ServiceReceipt) error {
	if len(services) > 64 {
		return errors.New("project service history exceeds 64 services")
	}
	for key, receipt := range services {
		if ValidateName(key) != nil || !validDigest(receipt.ID, 32) || !validDigest(receipt.Spec, 64) || len(receipt.Fields) == 0 || len(receipt.Fields) > len(previewFields) {
			return errors.New("invalid project service comparison identity")
		}
		for name, value := range receipt.Fields {
			if !previewFields[name] || !validDigest(value, 64) {
				return errors.New("invalid project service comparison fields")
			}
		}
	}
	return nil
}

func (runner *Runner) serviceChange(service Service, record container.Record, found, matches, selected bool) Change {
	change := Change{Kind: "service", Name: service.Key, Action: "create", Reason: "service_missing"}
	if !found {
		return change
	}
	change.ID = record.ID
	change.Action, change.Reason = "reuse", "configuration_matches"
	if record.State != container.StateRunning {
		change.Action, change.Reason = "start", "service_stopped"
	}
	if matches && !selected {
		if service.Task {
			change.Action, change.Reason = "reuse", "task_running"
			if record.Terminal() {
				_, err := taskResult(service.Key, record.State, record.ExitCode, record.ExitReason, record.CleanupFailures)
				if err == nil {
					change.Reason = "task_completed"
				} else {
					change.Action, change.Reason = "blocked", "task_failed"
				}
			}
		}
		return change
	}
	change.Action, change.Reason = "recreate", "explicit_recreate"
	if matches {
		return change
	}
	change.Reason, change.RequiresRecreate = "configuration_changed", !selected
	receipt, ok := runner.State.Services[service.Key]
	if !ok || receipt.ID != record.ID || receipt.Spec != record.Labels[LabelSpec] || len(service.Fields) == 0 {
		change.DetailsUnavailable = true
		return change
	}
	keys := map[string]bool{}
	for name := range receipt.Fields {
		keys[name] = true
	}
	for name := range service.Fields {
		keys[name] = true
	}
	for name := range keys {
		if receipt.Fields[name] != service.Fields[name] {
			change.ChangedFields = append(change.ChangedFields, name)
		}
	}
	sort.Strings(change.ChangedFields)
	if len(change.ChangedFields) == 0 {
		change.DetailsUnavailable = true
	}
	return change
}

func (runner *Runner) rememberServices(plan Plan, records []container.Record) error {
	services := map[string]ServiceReceipt{}
	for _, record := range records {
		for _, service := range plan.Services {
			if record.Labels[LabelService] == service.Key && len(service.Fields) > 0 {
				services[service.Key] = ServiceReceipt{ID: record.ID, Spec: record.Labels[LabelSpec], Fields: maps.Clone(service.Fields)}
			}
		}
	}
	if maps.EqualFunc(services, runner.State.Services, func(a, b ServiceReceipt) bool {
		return a.ID == b.ID && a.Spec == b.Spec && maps.Equal(a.Fields, b.Fields)
	}) {
		return nil
	}
	state := runner.State
	state.Services = services
	if err := runner.Save(state); err != nil {
		return err
	}
	runner.State = state
	return nil
}

func (runner *Runner) forgetService(record container.Record) error {
	key := record.Labels[LabelService]
	if receipt, ok := runner.State.Services[key]; !ok || receipt.ID != record.ID {
		return nil
	}
	state := runner.State
	state.Services = maps.Clone(state.Services)
	delete(state.Services, key)
	if err := runner.Save(state); err != nil {
		return err
	}
	runner.State = state
	return nil
}
