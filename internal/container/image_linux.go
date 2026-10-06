//go:build linux

package container

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/mingo-liu/mini-docker/internal/config"
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
		if cfg.Image == id {
			return true, nil
		}
	}
	return false, nil
}
