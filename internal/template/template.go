// Package template resolves directory and image sources for container startup.
// It owns image leases; rootfs owns validation, copying, and mount operations.
package template

import (
	"context"
	"errors"
	"io"

	"github.com/mingo-liu/mini-docker/internal/config"
	"github.com/mingo-liu/mini-docker/internal/image"
	"github.com/mingo-liu/mini-docker/internal/rootfs"
)

// Template is a validated, canonical rootfs source. Keep it open until copying
// or publishing a durable image reference completes. Directory sources must
// remain stable for that duration; image sources are protected from removal.
type Template struct {
	Path  string
	lease io.Closer
}

// Close releases the image lease, if any. It is safe to call more than once.
func (t *Template) Close() error {
	if t.lease == nil {
		return nil
	}
	lease := t.lease
	t.lease = nil
	return lease.Close()
}

// Acquire resolves the configured source and validates all host bind sources.
// On failure, any acquired image lease is released before returning.
func Acquire(ctx context.Context, cfg config.Config) (*Template, error) {
	return acquire(ctx, cfg, acquireImage)
}

func acquireImage(ctx context.Context, id string) (string, io.Closer, error) {
	store, err := image.OpenStore()
	if err != nil {
		return "", nil, err
	}
	_, path, lease, err := store.Acquire(ctx, id)
	if err != nil {
		return "", nil, err
	}
	return path, lease, nil
}

func acquire(ctx context.Context, cfg config.Config, resolveImage func(context.Context, string) (string, io.Closer, error)) (_ *Template, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	t := &Template{Path: cfg.RootFS}
	defer func() {
		if err != nil {
			err = errors.Join(err, t.Close())
		}
	}()
	if cfg.Image != "" {
		t.Path, t.lease, err = resolveImage(ctx, cfg.Image)
		if err != nil {
			return nil, err
		}
	}
	t.Path, err = rootfs.Validate(t.Path)
	if err != nil {
		return nil, err
	}
	if cfg.Image == "" && image.IsStorePath(t.Path) {
		return nil, errors.New("stored image filesystems require --image")
	}
	if err := rootfs.ValidateMountSources(cfg.Mounts, t.Path); err != nil {
		return nil, err
	}
	return t, nil
}
