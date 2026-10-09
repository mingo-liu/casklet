package template

import (
	"context"
	"errors"
	"io"
	"os"

	"github.com/mingo-liu/casklet/internal/config"
	"github.com/mingo-liu/casklet/internal/image"
)

// ResolveExecution resolves a name to an immutable identity and applies its
// startup defaults. The caller holds the lease until runtime source acquisition
// or durable container creation completes; no tag lookup is needed on restart.
func ResolveExecution(ctx context.Context, cfg config.Config, entrypoint *string) (config.Config, io.Closer, error) {
	store, err := image.OpenStore()
	if err != nil {
		return cfg, nil, err
	}
	return resolveExecution(ctx, cfg, entrypoint, store)
}

type imageSource interface {
	Resolve(context.Context, string) (image.Record, error)
	Pull(context.Context, string) (image.Record, error)
	Acquire(context.Context, string) (image.Record, string, *os.File, error)
}

func resolveExecution(ctx context.Context, cfg config.Config, entrypoint *string, store imageSource) (config.Config, io.Closer, error) {
	record, err := store.Resolve(ctx, cfg.Image)
	if errors.Is(err, image.ErrNotFound) && !image.IsIDReference(cfg.Image) {
		record, err = store.Pull(ctx, cfg.Image)
	}
	if err != nil {
		return cfg, nil, err
	}
	record, tree, lease, err := store.Acquire(ctx, record.ID)
	if err != nil {
		return cfg, nil, err
	}
	cfg.Image = record.ID
	if record.Config != nil && cfg.UserNS {
		lease.Close()
		return cfg, nil, errors.New("OCI images currently require execution without --userns or --rootless")
	}
	defaults := image.LaunchConfig{}
	if record.Config != nil {
		defaults = *record.Config
		cfg.OCI = true
	}
	cfg, err = defaults.Apply(cfg, tree, entrypoint)
	if err != nil {
		lease.Close()
		return cfg, nil, err
	}
	return cfg, lease, nil
}
