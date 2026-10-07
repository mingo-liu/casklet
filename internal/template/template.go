// Package template resolves directory and image sources for container startup.
// It owns source leases; rootfs owns validation, copying, and mount operations.
package template

import (
	"context"
	"errors"
	"io"
	"path/filepath"

	"github.com/mingo-liu/mini-docker/internal/config"
	"github.com/mingo-liu/mini-docker/internal/image"
	"github.com/mingo-liu/mini-docker/internal/rootfs"
)

// BuiltinPath is the managed BusyBox source on the product VM's Linux disk.
const BuiltinPath = "/var/lib/mini-docker/templates/busybox"

// Template is a validated, canonical rootfs source. Keep it open until copying
// or publishing a durable image reference completes. Directory sources must
// remain stable for that duration; builtin and image sources have source leases.
type Template struct {
	Path      string
	lease     io.Closer
	copyLease io.Closer
}

// ReleaseCopyLease permits builtin template repair after a private copy is ready.
// Image leases remain held until Close. Both methods are idempotent.
func (t *Template) ReleaseCopyLease() error {
	if t.copyLease == nil {
		return nil
	}
	lease := t.copyLease
	t.copyLease = nil
	return lease.Close()
}

// Close releases all source leases. It is safe to call more than once.
func (t *Template) Close() error {
	copyErr := t.ReleaseCopyLease()
	if t.lease == nil {
		return copyErr
	}
	lease := t.lease
	t.lease = nil
	return errors.Join(copyErr, lease.Close())
}

// Acquire resolves the configured source and validates all host bind sources.
// On failure, any acquired source leases are released before returning.
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
	} else if filepath.Clean(cfg.RootFS) == BuiltinPath {
		t.copyLease, err = lockBuiltin(ctx, false)
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
