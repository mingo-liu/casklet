package cli

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"

	"github.com/mingo-liu/casklet/internal/container"
	"github.com/mingo-liu/casklet/internal/project"
)

const projectRecordPageSize = 64

type projectRecordPage struct {
	Records []container.Record `json:"records"`
	Next    string             `json:"next,omitempty"`
}

func validProjectCursor(cursor string) bool {
	if cursor == "" {
		return true
	}
	if len(cursor) != 32 {
		return false
	}
	_, err := hex.DecodeString(cursor)
	return err == nil && strings.ToLower(cursor) == cursor
}

func parseProjectRecords(args []string) (Request, error) {
	if len(args) < 2 || len(args) > 3 || args[1] != "project" && args[1] != "inventory" {
		return Request{}, errors.New("invalid project record selection")
	}
	if err := project.ValidateName(args[0]); err != nil {
		return Request{}, err
	}
	r := Request{Action: "project-records", Reference: args[0], All: args[1] == "inventory"}
	if len(args) == 3 {
		r.Cursor = args[2]
		if r.Cursor == "" || !validProjectCursor(r.Cursor) {
			return Request{}, errors.New("invalid project record cursor")
		}
	}
	return r, nil
}

func executeProjectRecords(ctx context.Context, r Request, out io.Writer) error {
	records, err := container.List(ctx, true)
	if err != nil {
		return err
	}
	return writeProjectRecords(out, records, r.Reference, r.Cursor, r.All)
}

func writeProjectRecords(out io.Writer, records []container.Record, name, cursor string, inventory bool) error {
	sort.Slice(records, func(i, j int) bool { return records[i].ID < records[j].ID })
	page := projectRecordPage{Records: []container.Record{}}
	for _, record := range records {
		if record.ID <= cursor || !inventory && record.Labels[project.LabelName] != name {
			continue
		}
		if len(page.Records) == projectRecordPageSize {
			page.Next = page.Records[len(page.Records)-1].ID
			break
		}
		if record.Labels[project.LabelName] != name {
			// Only name ownership and reservation identity are needed outside
			// the project. Commands, labels and private diagnostics stay guest-local.
			record = container.Record{ID: record.ID, Name: record.Name, State: record.State, Generation: record.Generation, CreatedAt: record.CreatedAt}
		}
		page.Records = append(page.Records, record)
	}
	return json.NewEncoder(out).Encode(page)
}

// Keep the orchestrator's list interface while paging its host/guest transport.
// Each response fits the existing per-operation bound independently of how much
// unrelated retained command or label data exists on the machine.
func projectRecordCalls(name string, call project.Call) project.Call {
	return func(ctx context.Context, args ...string) ([]byte, error) {
		if len(args) == 0 || args[0] != "ps" {
			return call(ctx, args...)
		}
		scope := "inventory"
		if len(args) < 3 || args[1] != "-a" || args[2] != "--json" {
			return nil, errors.New("invalid project inventory request")
		}
		if len(args) == 5 && args[3] == "--filter" && args[4] == "label="+project.LabelName+"="+name {
			scope = "project"
		} else if len(args) != 3 {
			return nil, errors.New("invalid project inventory request")
		}
		records := []container.Record{}
		cursor := ""
		for {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			pageArgs := []string{"__project-records", name, scope}
			if cursor != "" {
				pageArgs = append(pageArgs, cursor)
			}
			data, err := call(ctx, pageArgs...)
			if err != nil {
				return nil, err
			}
			var page projectRecordPage
			if json.Unmarshal(data, &page) != nil || page.Records == nil || len(page.Records) > projectRecordPageSize {
				return nil, errors.New("invalid project inventory response")
			}
			previous := cursor
			for _, record := range page.Records {
				if !validProjectCursor(record.ID) || record.ID == "" || record.ID <= previous || scope == "project" && record.Labels[project.LabelName] != name {
					return nil, errors.New("invalid project inventory identity")
				}
				previous = record.ID
			}
			if page.Next != "" && (len(page.Records) != projectRecordPageSize || page.Next != previous) {
				return nil, errors.New("invalid project inventory continuation")
			}
			records = append(records, page.Records...)
			if page.Next == "" {
				// Restore ps creation order after ID-based traversal, including
				// the reverse creation order used by transaction rollback.
				sort.Slice(records, func(i, j int) bool {
					if records[i].CreatedAt.Equal(records[j].CreatedAt) {
						return records[i].ID < records[j].ID
					}
					return records[i].CreatedAt.Before(records[j].CreatedAt)
				})
				return json.Marshal(records)
			}
			cursor = page.Next
		}
	}
}
