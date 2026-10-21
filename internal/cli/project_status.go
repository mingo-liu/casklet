package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/mingo-liu/casklet/internal/container"
	"github.com/mingo-liu/casklet/internal/project"
)

func parseProjectStatus(args []string) (Request, error) {
	if len(args) < 1 || len(args) > 1+container.RecordPageSize {
		return Request{}, errors.New("project status requires a project name and at most 64 full IDs")
	}
	if err := project.ValidateName(args[0]); err != nil {
		return Request{}, err
	}
	seen := map[string]bool{}
	for _, id := range args[1:] {
		if id == "" || !validProjectCursor(id) || seen[id] {
			return Request{}, errors.New("invalid project status ID selection")
		}
		seen[id] = true
	}
	return Request{Action: "project-status", Reference: args[0], References: args[1:]}, nil
}

func executeProjectStatus(ctx context.Context, r Request, out io.Writer) error {
	var records []container.Record
	var err error
	if len(r.References) == 0 {
		// Retain the private name-only sampler for existing diagnostic callers.
		records, err = container.List(ctx, true)
	} else {
		records, err = container.ListIDs(ctx, r.References)
	}
	if err != nil {
		return err
	}
	return writeProjectStatus(out, project.Managed(records, r.Reference))
}

func writeProjectStatus(out io.Writer, records []container.Record) error {
	if len(records) > 64 {
		return errors.New("project status supports at most 64 services")
	}
	result := make([]project.ServiceStatus, 0, len(records))
	for _, record := range records {
		status := project.ServiceStatus{ID: record.ID, Project: record.Labels[project.LabelName], Service: record.Labels[project.LabelService], Generation: record.Generation, State: record.State}
		if record.Health != nil {
			status.Health = record.Health.Status
		}
		result = append(result, status)
	}
	return json.NewEncoder(out).Encode(result)
}
