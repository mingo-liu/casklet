package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/mingo-liu/casklet/internal/container"
	"github.com/mingo-liu/casklet/internal/project"
)

func executeProjectStatus(ctx context.Context, name string, out io.Writer) error {
	records, err := container.List(ctx, true)
	if err != nil {
		return err
	}
	return writeProjectStatus(out, project.Managed(records, name))
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
