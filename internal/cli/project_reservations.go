package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/mingo-liu/casklet/internal/config"
	"github.com/mingo-liu/casklet/internal/container"
	"github.com/mingo-liu/casklet/internal/project"
)

func parseProjectReservations(args []string) (Request, error) {
	if len(args) < 1 || len(args) > 65 {
		return Request{}, errors.New("invalid project reservation selection")
	}
	r := Request{Action: "project-reservations", Cursor: args[0]}
	if r.Cursor == "-" {
		r.Cursor = ""
	} else if r.Cursor == "" || !validProjectCursor(r.Cursor) {
		return Request{}, errors.New("invalid project reservation cursor")
	}
	seen := map[string]bool{}
	for _, id := range args[1:] {
		if id == "" || !validProjectCursor(id) || seen[id] {
			return Request{}, errors.New("invalid project reservation exclusion")
		}
		seen[id] = true
		r.References = append(r.References, id)
	}
	return r, nil
}

func executeProjectReservations(ctx context.Context, r Request, out io.Writer) error {
	page, err := container.ListPage(ctx, container.RecordPageOptions{After: r.Cursor, ExcludeIDs: r.References})
	if err != nil {
		return err
	}
	store, err := container.OpenStoreContext(ctx)
	if err != nil {
		return err
	}
	return writeProjectReservations(ctx, out, page, store.Snapshot)
}

func writeProjectReservations(ctx context.Context, out io.Writer, records container.RecordPage, snapshot func(context.Context, string) (container.Record, config.Config, error)) error {
	page := project.ReservationPage{Records: []project.Reservation{}, Next: records.Next}
	for _, record := range records.Records {
		if err := ctx.Err(); err != nil {
			return err
		}
		current, cfg, err := snapshot(ctx, record.ID)
		if err != nil {
			return err
		}
		if current.ID != record.ID || current.Generation != record.Generation {
			return errors.New("container execution changed during reservation sampling")
		}
		page.Records = append(page.Records, project.Reservation{ID: current.ID, Name: current.Name, State: current.State, Generation: current.Generation,
			Network: cfg.Network, NetworkAliases: cfg.NetworkAliases, Publish: cfg.Publish})
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(page)
}
