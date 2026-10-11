package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/mingo-liu/casklet/internal/container"
	"github.com/mingo-liu/casklet/internal/network"
	"github.com/mingo-liu/casklet/internal/project"
	"github.com/mingo-liu/casklet/internal/volume"
)

// The private host/guest protocol returns exact creation receipts and compares
// deletion identities under the existing resource metadata locks.
func parseProjectResource(args []string) (Request, error) {
	if len(args) < 3 || args[1] != "network" && args[1] != "volume" {
		return Request{}, errors.New("invalid project resource request")
	}
	r := Request{Action: "project-resource", References: append([]string(nil), args...)}
	switch args[0] {
	case "create":
		if len(args) != 3 {
			return Request{}, errors.New("invalid project resource creation")
		}
	case "rm":
		if len(args) != 4 {
			return Request{}, errors.New("invalid project resource removal")
		}
		created, err := time.Parse(time.RFC3339Nano, args[3])
		if err != nil || created.IsZero() {
			return Request{}, errors.New("invalid project resource identity")
		}
	default:
		return Request{}, errors.New("invalid project resource operation")
	}
	return r, project.ValidateName(args[2])
}

func executeProjectResource(ctx context.Context, r Request, out io.Writer) (int, error) {
	args := r.References
	receipt := project.Resource{Kind: args[1], Name: args[2]}
	if args[1] == "network" {
		store, err := network.OpenStore()
		if err != nil {
			return 125, err
		}
		if args[0] == "rm" {
			created, _ := time.Parse(time.RFC3339Nano, args[3])
			return 0, store.RemoveIfCreated(ctx, args[2], created, container.NetworkReferenced)
		}
		record, err := store.CreateExclusive(ctx, args[2])
		if err != nil {
			return 125, err
		}
		receipt.CreatedAt = record.CreatedAt
	} else {
		store, err := volume.OpenStore()
		if err != nil {
			return 125, err
		}
		if args[0] == "rm" {
			created, _ := time.Parse(time.RFC3339Nano, args[3])
			return 0, store.RemoveIfCreated(ctx, args[2], created, container.VolumeReferenced)
		}
		record, err := store.CreateExclusive(ctx, args[2])
		if err != nil {
			return 125, err
		}
		receipt.CreatedAt = record.CreatedAt
	}
	return 0, json.NewEncoder(out).Encode(receipt)
}
