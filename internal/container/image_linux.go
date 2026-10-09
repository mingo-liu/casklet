//go:build linux

package container

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/mingo-liu/casklet/internal/config"
)

// ImageReferenced checks all retained records, including completed and failed
// containers. Lock order is image store, then container store; never the reverse.
func ImageReferenced(ctx context.Context, id string) (bool, error) {
	store, err := OpenStore()
	if err != nil {
		return false, err
	}
	return store.imageReferenced(ctx, id)
}

func (store *Store) imageReferenced(ctx context.Context, id string) (bool, error) {
	return store.configReferenced(ctx, func(cfg config.Config) bool { return cfg.Image == id })
}

// VolumeReferenced includes stopped containers; lock order is volume then container.
func VolumeReferenced(ctx context.Context, name string) (bool, error) {
	store, err := OpenStore()
	if err != nil {
		return false, err
	}
	return store.configReferenced(ctx, func(cfg config.Config) bool {
		for _, m := range cfg.Mounts {
			if m.Type == "volume" && m.Source == name {
				return true
			}
		}
		return false
	})
}

func (store *Store) configReferenced(ctx context.Context, matches func(config.Config) bool) (bool, error) {
	lock, err := store.lock(ctx, true)
	if err != nil {
		return false, err
	}
	defer lock.Close()
	records, err := store.listLocked()
	if err != nil {
		return false, err
	}
	for _, record := range records {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		var cfg config.Config
		if err := store.readJSON(filepath.Join(store.root, record.ID, "config.json"), maxConfigBytes, &cfg); err != nil {
			return false, err
		}
		if err := cfg.ValidateExecution(); err != nil {
			return false, errors.New("invalid stored container configuration")
		}
		if matches(cfg) {
			return true, nil
		}
	}
	return false, nil
}

// NetworkReferenced includes stopped containers. Lock order is network then container.
func NetworkReferenced(ctx context.Context, name string) (bool, error) {
	s, err := OpenStore()
	if err != nil {
		return false, err
	}
	return s.configReferenced(ctx, func(c config.Config) bool { return c.Network == name })
}
