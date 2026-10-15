//go:build linux

package container

import (
	"context"
	"errors"

	"github.com/mingo-liu/casklet/internal/image"
	"github.com/mingo-liu/casklet/internal/rootfs"
)

// RemoveProjectService fences the exact receipt before any stop, and keeps the
// existing operation lock throughout stop and removal. Labels pin ownership.
func RemoveProjectService(ctx context.Context, id string, generation uint64, labels map[string]string) error {
	store, record, operation, err := lockReference(ctx, id)
	if err != nil {
		return err
	}
	defer operation.Close()
	if err := verifyProjectService(record, id, generation, labels); err != nil {
		return err
	}
	if err := markUserStopped(ctx, store, id); err != nil {
		return err
	}
	record, err = stopLocked(ctx, store, record, nil)
	if err != nil {
		return err
	}
	return removeLocked(ctx, store, record)
}

func verifyProjectService(record Record, id string, generation uint64, labels map[string]string) error {
	if record.ID != id || record.Generation != generation {
		return errors.New("project service execution changed before replacement; retry")
	}
	for key, value := range labels {
		if record.Labels[key] != value {
			return errors.New("project service ownership changed before replacement")
		}
	}
	return nil
}

// PreflightProjectStart validates the actual saved retained source rather than
// reopening a possibly removed original template or merging a mutable tag.
func PreflightProjectStart(ctx context.Context, id string, generation uint64, labels map[string]string) error {
	store, record, operation, err := lockReference(ctx, id)
	if err != nil {
		return err
	}
	defer operation.Close()
	if err := verifyProjectService(record, id, generation, labels); err != nil {
		return err
	}
	cfg, err := store.Config(ctx, id)
	if err != nil {
		return err
	}
	retained, err := store.RootFS(ctx, id)
	if err != nil {
		return err
	}
	candidate, needsImage, err := retainedRootCandidate(cfg, retained)
	if err != nil {
		return err
	}
	if needsImage {
		images, err := image.OpenStore()
		if err != nil {
			return err
		}
		_, tree, lease, err := images.Acquire(ctx, cfg.Image)
		if err != nil {
			return err
		}
		defer lease.Close()
		candidate = tree
	}
	if _, err := rootfs.Validate(candidate); err != nil {
		return err
	}
	if err := rootfs.CheckUnmounted(retained); err != nil {
		return err
	}
	return rootfs.ValidateMountSources(cfg.Mounts, candidate)
}
