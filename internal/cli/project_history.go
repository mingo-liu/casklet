package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"

	"github.com/mingo-liu/casklet/internal/config"
	"github.com/mingo-liu/casklet/internal/container"
	"github.com/mingo-liu/casklet/internal/project"
	"github.com/mingo-liu/casklet/internal/runargs"
)

func deploymentContainers(plan project.Plan, records []container.Record) map[string]string {
	ids := map[string]string{}
	for _, service := range plan.Services {
		for _, record := range records {
			if record.Name == service.Name && record.Labels[project.LabelName] == plan.Name && record.Labels[project.LabelService] == service.Key {
				ids[service.Key] = record.ID
				break
			}
		}
	}
	return ids
}

func deploymentImages(ctx context.Context, plan project.Plan, records []container.Record, call project.Call) (map[string]string, error) {
	images := map[string]string{}
	for _, service := range plan.Services {
		if service.Config.Image == "" {
			continue
		}
		var id string
		for _, record := range records {
			if record.Name == service.Name && record.Labels[project.LabelService] == service.Key {
				id = record.ID
				break
			}
		}
		if id == "" {
			return nil, errors.New("deployed service missing while recording history")
		}
		data, err := call(ctx, "inspect", id)
		if err != nil {
			return nil, err
		}
		var inspected container.Inspection
		if json.Unmarshal(data, &inspected) != nil || inspected.ID != id || inspected.Name != service.Name || inspected.Labels[project.LabelName] != plan.Name || inspected.Labels[project.LabelService] != service.Key || config.ValidateImageID(inspected.Config.Image) != nil {
			return nil, errors.New("invalid deployed image identity")
		}
		images[service.Key] = inspected.Config.Image
	}
	return images, nil
}

// Restore only expanded run arguments: original config/env files are never
// reopened. Revalidate their effective values before replacing image references
// and spec labels, so unsupported/default changes fail before any mutations.
func restoreDeployment(d project.Deployment) (project.Plan, error) {
	raw, err := json.Marshal(d.Plan)
	if err != nil {
		return project.Plan{}, err
	}
	var plan project.Plan
	if err := json.Unmarshal(raw, &plan); err != nil {
		return plan, err
	}
	doc := project.Document{Name: plan.Name, Networks: []string{}, Volumes: []string{}, ExternalVolumes: map[string]string{}, Services: map[string]project.ServiceConfig{}}
	for _, s := range plan.Services {
		depends := s.DependsOn
		if depends == nil {
			depends = map[string]string{}
		}
		doc.Services[s.Key] = project.ServiceConfig{Task: s.Task, Run: json.RawMessage(`{}`), DependsOn: depends}
	}
	graph, _ := json.Marshal(doc)
	decoded, err := project.Decode(graph)
	if err != nil {
		return plan, err
	}
	order, err := decoded.Order()
	if err != nil {
		return plan, err
	}
	for i := range plan.Services {
		service := &plan.Services[i]
		if service.Key != order[i] {
			return plan, errors.New("invalid deployment dependency order")
		}
		args := append([]string(nil), service.Args...)
		// Saved arguments must be completely expanded. Reject file expansion before
		// calling Parse, even if a state file was manually edited.
		err := runargs.Walk(args, func(name, value string, index, end int) error {
			if name == "config" || name == "env-file" {
				return errors.New("deployment contains unexpanded local files")
			}
			return nil
		})
		if err != nil {
			return plan, err
		}
		request, err := Parse(args)
		if err != nil {
			return plan, errors.New("saved deployment is incompatible with current run options")
		}
		if request.Action != "run" || !request.Detach || request.Name != service.Name || request.Config.Labels[project.LabelName] != plan.Name || request.Config.Labels[project.LabelService] != service.Key || request.Config.Labels[project.LabelSpec] != service.Fingerprint {
			return plan, errors.New("saved deployment run identity mismatch")
		}
		cfg := request.Config
		for _, key := range []string{project.LabelName, project.LabelService, project.LabelSpec, project.LabelTransaction} {
			delete(cfg.Labels, key)
		}
		if len(cfg.Labels) == 0 {
			cfg.Labels = nil
		}
		cfg.Env = canonicalProjectEnvironment(cfg.Env)
		sort.Strings(cfg.NetworkAliases)
		currentConfig, _ := json.Marshal(cfg)
		savedConfig, _ := json.Marshal(service.Config)
		if !bytes.Equal(currentConfig, savedConfig) || projectFingerprint(cfg, request.Entrypoint) != service.Fingerprint {
			return plan, errors.New("saved deployment defaults have changed; deploy an explicit configuration instead")
		}
		if service.Task != (cfg.Labels[project.LabelTask] == "true") {
			return plan, errors.New("saved deployment task identity mismatch")
		}
		imageID, hasImage := d.Images[service.Key]
		if hasImage != (cfg.Image != "") || hasImage && config.ValidateImageID(imageID) != nil {
			return plan, errors.New("saved deployment image identity mismatch")
		}
		if hasImage {
			cfg.Image = imageID
		}
		service.Config = cfg
		service.Fingerprint = projectFingerprint(cfg, request.Entrypoint)
		service.Fields = project.FieldDigests(cfg, request.Entrypoint)
		err = runargs.Walk(args, func(name, value string, index, end int) error {
			if name == "image" {
				runargs.SetValue(args, index, end, imageID)
			}
			if name == "label" {
				key, _, _ := strings.Cut(value, "=")
				if key == project.LabelSpec {
					runargs.SetValue(args, index, end, key+"="+service.Fingerprint)
				}
			}
			return nil
		})
		if err != nil {
			return plan, err
		}
		service.Args = args
	}
	for _, names := range [][]string{plan.Networks, plan.Volumes} {
		if len(names) > 16 {
			return plan, errors.New("too many saved deployment resources")
		}
		seen := map[string]bool{}
		for _, name := range names {
			if project.ValidateName(name) != nil || !strings.HasPrefix(name, "cp-"+plan.Name+"-") || seen[name] {
				return plan, errors.New("invalid saved deployment resource")
			}
			seen[name] = true
		}
	}
	if len(plan.Volumes)+len(plan.ExternalVolumes) > 16 {
		return plan, errors.New("too many saved deployment volumes")
	}
	for _, name := range plan.ExternalVolumes {
		if config.ValidateVolumeName(name) != nil || slices.Contains(plan.Volumes, name) {
			return plan, errors.New("invalid saved external volume")
		}
	}
	for _, service := range plan.Services {
		if service.Config.NetworkMode() != "none" && !slices.Contains(plan.Networks, service.Config.Network) {
			return plan, errors.New("saved service uses an undeclared network")
		}
		for _, name := range config.VolumeNames(service.Config.Mounts) {
			if !slices.Contains(plan.Volumes, name) && !slices.Contains(plan.ExternalVolumes, name) {
				return plan, errors.New("saved service uses an undeclared volume")
			}
		}
	}
	return plan, nil
}

func writeDeploymentHistory(out io.Writer, h project.History, jsonOutput bool) error {
	summaries := h.Summaries()
	if jsonOutput {
		return json.NewEncoder(out).Encode(summaries)
	}
	for _, d := range summaries {
		restored := ""
		if d.RestoredFrom != 0 {
			restored = fmt.Sprintf(" restored-from=%d", d.RestoredFrom)
		}
		if _, err := fmt.Fprintf(out, "%d %s services=%s%s\n", d.Revision, d.CreatedAt.Format("2006-01-02T15:04:05Z07:00"), strings.Join(d.Services, ","), restored); err != nil {
			return err
		}
	}
	return nil
}
