package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/mingo-liu/casklet/internal/container"
	"github.com/mingo-liu/casklet/internal/project"
)

func TestProjectStatusExcludesUnboundedPrivateFields(t *testing.T) {
	command := strings.Repeat("private-command", 1<<16)
	records := make([]container.Record, 64)
	for i := range records {
		records[i] = container.Record{ID: strings.Repeat("a", 32), Generation: 1, State: container.StateRunning, Labels: map[string]string{project.LabelName: "demo", project.LabelService: "api", project.LabelSpec: "private-config-digest"}, Command: []string{command}, Health: &container.Health{Status: container.HealthHealthy}}
	}
	var out bytes.Buffer
	if err := writeProjectStatus(&out, records); err != nil {
		t.Fatal(err)
	}
	if out.Len() > 32<<10 || strings.Contains(out.String(), "private") {
		t.Fatalf("unbounded/private status: %d bytes", out.Len())
	}
	var statuses []project.ServiceStatus
	if err := json.Unmarshal(out.Bytes(), &statuses); err != nil || len(statuses) != 64 || statuses[0].Health != container.HealthHealthy {
		t.Fatalf("status: %v %v", statuses, err)
	}
	if err := writeProjectStatus(&out, append(records, container.Record{})); err == nil {
		t.Fatal("unbounded services accepted")
	}
	for _, args := range [][]string{{"__project-status"}, {"__project-status", "demo", "extra"}, {"__project-status", "../demo"}} {
		if _, err := Parse(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	r, err := Parse([]string{"__project-status", "demo"})
	if err != nil || r.Action != "project-status" || r.Reference != "demo" {
		t.Fatalf("parse: %+v %v", r, err)
	}
}

func TestProjectStatusPinnedSelectionBounds(t *testing.T) {
	ids := make([]string, container.RecordPageSize)
	for i := range ids {
		ids[i] = fmt.Sprintf("%032x", i+1)
	}
	for _, count := range []int{1, container.RecordPageSize} {
		args := append([]string{"__project-status", "demo"}, ids[:count]...)
		r, err := Parse(args)
		if err != nil || r.Action != "project-status" || r.Reference != "demo" || !slices.Equal(r.References, ids[:count]) {
			t.Fatalf("pinned selection: %+v %v", r, err)
		}
	}
	for _, selection := range [][]string{{""}, {ids[0][:12]}, {"container-name"}, {strings.Repeat("A", 32)}, {ids[0], ids[0]}, append(slices.Clone(ids), fmt.Sprintf("%032x", 65))} {
		if _, err := Parse(append([]string{"__project-status", "demo"}, selection...)); err == nil {
			t.Fatalf("accepted invalid selection: %v", selection)
		}
	}
}
