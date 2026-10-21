package project

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/mingo-liu/casklet/internal/config"
	"github.com/mingo-liu/casklet/internal/container"
)

const ReservationPageSize = 64

// Reservation excludes commands, environment, health configuration and paths.
type Reservation struct {
	ID             string               `json:"id"`
	Name           string               `json:"name"`
	State          string               `json:"state"`
	Generation     uint64               `json:"generation"`
	Network        string               `json:"network"`
	NetworkAliases []string             `json:"network_aliases,omitempty"`
	Publish        []config.PortMapping `json:"publish,omitempty"`
}

type ReservationPage struct {
	Records []Reservation `json:"records"`
	Next    string        `json:"next,omitempty"`
}

func (r Reservation) validate() error {
	if !validDigest(r.ID, 32) || container.ValidateName(r.Name) != nil {
		return errors.New("invalid project reservation identity")
	}
	switch r.State {
	case container.StateCreated, container.StateStarting, container.StateRunning, container.StateStopping, container.StateExited, container.StateFailed:
	default:
		return errors.New("invalid project reservation state")
	}
	if err := (config.Config{Network: r.Network, NetworkAliases: r.NetworkAliases}).ValidateNetworkAliases(); err != nil {
		return errors.New("invalid project network reservation")
	}
	if err := config.ValidateNetwork(r.Network, nil, r.Publish, nil); err != nil {
		return errors.New("invalid project port reservation")
	}
	return nil
}

func (runner *Runner) reservations(ctx context.Context, all []container.Record, removed map[string]bool) ([]Reservation, error) {
	expected := make(map[string]container.Record, len(all))
	for _, record := range all {
		if !removed[record.ID] {
			expected[record.ID] = record
		}
	}
	if len(expected) == 0 {
		return []Reservation{}, ctx.Err()
	}
	skip := make([]string, 0, len(removed))
	for id := range removed {
		skip = append(skip, id)
	}
	sort.Strings(skip)
	result := []Reservation{}
	cursor := ""
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		after := cursor
		if after == "" {
			after = "-"
		}
		args := append([]string{"__project-reservations", after}, skip...)
		data, err := runner.Call(ctx, args...)
		if err != nil {
			return nil, err
		}
		var page ReservationPage
		if json.Unmarshal(data, &page) != nil || page.Records == nil || len(page.Records) > ReservationPageSize {
			return nil, errors.New("invalid project reservation response")
		}
		previous := cursor
		for _, saved := range page.Records {
			if err := saved.validate(); err != nil {
				return nil, err
			}
			if saved.ID <= previous || removed[saved.ID] {
				return nil, errors.New("invalid project reservation selection")
			}
			previous = saved.ID
			if record, found := expected[saved.ID]; found {
				if saved.Name != record.Name || saved.Generation != record.Generation {
					return nil, fmt.Errorf("container %q changed during project preflight", record.Name)
				}
				// Preserve the original inventory's conservative port claims.
				saved.State = record.State
				delete(expected, saved.ID)
			}
			result = append(result, saved)
		}
		if page.Next != "" && (len(page.Records) != ReservationPageSize || page.Next != previous) {
			return nil, errors.New("invalid project reservation continuation")
		}
		if page.Next == "" {
			if len(expected) != 0 {
				return nil, errors.New("containers disappeared during project preflight")
			}
			return result, ctx.Err()
		}
		cursor = page.Next
	}
}
