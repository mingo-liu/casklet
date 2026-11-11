// Package project owns bounded deployment plans and project resource ownership.
package project

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"

	"github.com/mingo-liu/casklet/internal/config"
)

const (
	LabelPrefix      = "io.casklet.project."
	LabelName        = LabelPrefix + "name"
	LabelService     = LabelPrefix + "service"
	LabelSpec        = LabelPrefix + "spec"
	LabelTransaction = LabelPrefix + "transaction"
	LabelTask        = LabelPrefix + "task"
)

var _dnsName = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

type Document struct {
	Name            string                   `json:"name"`
	Networks        []string                 `json:"networks"`
	Volumes         []string                 `json:"volumes"`
	Services        map[string]ServiceConfig `json:"services"`
	ExternalVolumes map[string]string        `json:"external-volumes"`
}

type ServiceConfig struct {
	Task      bool              `json:"task,omitempty"`
	Run       json.RawMessage   `json:"run"`
	DependsOn map[string]string `json:"depends-on"`
}

type Service struct {
	Task   bool              `json:"Task"`
	Config config.Config     `json:"Config"`
	Fields map[string]string `json:"Fields"`

	Key         string            `json:"Key"`
	Name        string            `json:"Name"`
	Fingerprint string            `json:"Fingerprint"`
	Args        []string          `json:"Args"`
	DependsOn   map[string]string `json:"DependsOn"`
	// MatchLegacyFingerprint verifies an old ordered-alias hash using the
	// desired configuration and the inspected container's saved alias order.
	MatchLegacyFingerprint func(string, []string) bool `json:"-"`
}

type Plan struct {
	Name            string    `json:"Name"`
	Networks        []string  `json:"Networks"`
	Volumes         []string  `json:"Volumes"`
	ExternalVolumes []string  `json:"ExternalVolumes"` // Existing guest names, never project-owned.
	Services        []Service `json:"Services"`        // Topological order, with deterministic ties.
}

func ValidateName(name string) error {
	if !_dnsName.MatchString(name) {
		return errors.New("project identifiers require 1-63 lowercase DNS label characters")
	}
	return nil
}

func ScopedName(project, logical string) (string, error) {
	name := "cp-" + project + "-" + logical
	return name, ValidateName(name)
}

// Decode rejects duplicate keys at every level, nulls, excessive nesting and
// unknown fields before any local files or VM resources are touched.
func Decode(data []byte) (Document, error) {
	var doc Document
	if len(data) > 1<<20 {
		return doc, errors.New("project config exceeds 1 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := validateValue(decoder, 0); err != nil {
		return doc, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return doc, errors.New("unexpected data after project config")
	}
	decoder = json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&doc); err != nil {
		return doc, errors.New("invalid project fields or types")
	}
	if err := ValidateName(doc.Name); err != nil {
		return doc, err
	}
	if len(doc.Services) == 0 || len(doc.Services) > 64 {
		return doc, errors.New("project requires 1-64 services")
	}
	if doc.Networks == nil {
		doc.Networks = []string{"default"}
	}
	for _, names := range [][]string{doc.Networks, doc.Volumes} {
		if len(names) > 16 {
			return doc, errors.New("project supports at most 16 networks and 16 volumes")
		}
		seen := make(map[string]bool, len(names))
		for _, name := range names {
			if _, err := ScopedName(doc.Name, name); err != nil || ValidateName(name) != nil || seen[name] {
				return doc, errors.New("invalid, duplicate, or oversized project resource name")
			}
			seen[name] = true
		}
	}
	if len(doc.Volumes)+len(doc.ExternalVolumes) > 16 {
		return doc, errors.New("project supports at most 16 volumes including external volumes")
	}
	var (
		logicalNames = make(map[string]bool, len(doc.Volumes)+len(doc.ExternalVolumes))
		actualNames  = make(map[string]bool, len(doc.Volumes)+len(doc.ExternalVolumes))
	)
	for _, name := range doc.Volumes {
		logicalNames[name] = true
		actual, _ := ScopedName(doc.Name, name)
		actualNames[actual] = true
	}
	for logical, actual := range doc.ExternalVolumes {
		if ValidateName(logical) != nil || config.ValidateVolumeName(actual) != nil || logicalNames[logical] || actualNames[actual] {
			return doc, errors.New("invalid or conflicting external volume declaration")
		}
		logicalNames[logical] = true
		actualNames[actual] = true
	}
	for key, service := range doc.Services {
		if _, err := ScopedName(doc.Name, key); err != nil || ValidateName(key) != nil {
			return doc, errors.New("invalid or oversized service name")
		}
		if len(service.Run) == 0 || service.Run[0] != '{' {
			return doc, fmt.Errorf("service %q requires a run object", key)
		}
		for dependency, condition := range service.DependsOn {
			if _, exists := doc.Services[dependency]; !exists || dependency == key {
				return doc, fmt.Errorf("service %q has an invalid dependency", key)
			}
			if condition == "completed" && !doc.Services[dependency].Task {
				return doc, errors.New("completed dependencies require a task")
			}
			if doc.Services[dependency].Task && condition != "completed" {
				return doc, errors.New("task dependencies require completed condition")
			}
			if condition != "started" && condition != "healthy" && condition != "completed" {
				return doc, errors.New("dependency condition must be started, healthy, or completed")
			}
		}
	}
	_, err := doc.Order()
	return doc, err
}

func (doc Document) Order() ([]string, error) {
	var (
		order  []string
		status = make(map[string]int, len(doc.Services))
		visit  func(string) error
	)
	visit = func(key string) error {
		if status[key] == 1 {
			return errors.New("project dependencies contain a cycle")
		}
		if status[key] == 2 {
			return nil
		}
		status[key] = 1
		keys := make([]string, 0, len(doc.Services[key].DependsOn))
		for dep := range doc.Services[key].DependsOn {
			keys = append(keys, dep)
		}
		sort.Strings(keys)
		for _, dep := range keys {
			if err := visit(dep); err != nil {
				return err
			}
		}
		status[key] = 2
		order = append(order, key)
		return nil
	}
	keys := make([]string, 0, len(doc.Services))
	for key := range doc.Services {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if err := visit(key); err != nil {
			return nil, err
		}
	}
	return order, nil
}

// HasTasks determines whether up needs a completion deadline without --wait.
func (plan Plan) HasTasks() bool {
	for _, service := range plan.Services {
		if service.Task {
			return true
		}
	}
	return false
}

func validateValue(decoder *json.Decoder, depth int) error {
	if depth > 64 {
		return errors.New("project config nesting exceeds 64 levels")
	}
	token, err := decoder.Token()
	if err != nil {
		return errors.New("invalid project JSON")
	}
	if token == nil {
		return errors.New("project config cannot contain null")
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]bool)
		for decoder.More() {
			token, err := decoder.Token()
			if err != nil {
				return errors.New("invalid project JSON")
			}
			key, ok := token.(string)
			if !ok || seen[key] {
				return errors.New("duplicate project config key")
			}
			seen[key] = true
			if err := validateValue(decoder, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := validateValue(decoder, depth+1); err != nil {
				return err
			}
		}
	default:
		return errors.New("invalid project JSON delimiter")
	}
	_, err = decoder.Token()
	return err
}
