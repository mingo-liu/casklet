//go:build linux

package container

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mingo-liu/casklet/internal/config"
	"github.com/mingo-liu/casklet/internal/image"
	"github.com/mingo-liu/casklet/internal/rootfs"
	containerruntime "github.com/mingo-liu/casklet/internal/runtime"
	"golang.org/x/sys/unix"
)

func lockReference(ctx context.Context, ref string) (*Store, Record, *os.File, error) {
	store, err := managementStore()
	if err != nil {
		return nil, Record{}, nil, err
	}
	record, err := store.Get(ctx, ref)
	if err != nil {
		return nil, record, nil, err
	}
	operation, err := store.AcquireOperation(ctx, record.ID)
	if err != nil {
		return nil, record, nil, err
	}
	latest, err := store.Get(ctx, record.ID)
	if err != nil || latest.Generation != record.Generation {
		operation.Close()
		if err == nil {
			err = errors.New("container execution changed; retry the operation")
		}
		return nil, latest, nil, err
	}
	return store, latest, operation, nil
}

func Stop(ctx context.Context, ref string) (Record, error) { return StopWithTimeout(ctx, ref, nil) }

func StopWithTimeout(ctx context.Context, ref string, timeout *time.Duration) (Record, error) {
	if timeout != nil {
		if err := config.ValidateStopTimeout(*timeout); err != nil {
			return Record{}, err
		}
	}
	store, record, operation, err := lockReference(ctx, ref)
	if err != nil {
		return record, err
	}
	defer operation.Close()
	return stopLocked(ctx, store, record, timeout)
}

func Remove(ctx context.Context, ref string) error {
	store, record, operation, err := lockReference(ctx, ref)
	if err != nil {
		return err
	}
	defer operation.Close()
	return removeLocked(ctx, store, record)
}

// StartExisting is idempotent for running containers. Stopped containers resume
// their retained rootfs with a new execution receipt and transient resources.
func StartExisting(ctx context.Context, ref string) (Record, error) {
	return StartExistingWithPreflight(ctx, ref, nil)
}

// StartPreflight checks external resources while the immutable container's
// operation lock is held. A failed check must not publish a new generation.
type StartPreflight func(context.Context, []config.PortMapping) error

func StartExistingWithPreflight(ctx context.Context, ref string, preflight StartPreflight) (Record, error) {
	store, record, operation, err := lockReference(ctx, ref)
	if err != nil {
		return record, err
	}
	defer operation.Close()
	record, err = refreshRecord(ctx, store, record, false)
	if err != nil {
		return record, err
	}
	if record.State == StateRunning {
		return record, nil
	}
	if !record.Terminal() {
		return record, errors.New("container is still starting or stopping")
	}
	return startStopped(ctx, store, record, preflight)
}

func Restart(ctx context.Context, ref string, timeout *time.Duration) (Record, error) {
	return RestartWithPreflight(ctx, ref, timeout, nil)
}

func RestartWithPreflight(ctx context.Context, ref string, timeout *time.Duration, preflight StartPreflight) (Record, error) {
	if timeout != nil {
		if err := config.ValidateStopTimeout(*timeout); err != nil {
			return Record{}, err
		}
	}
	store, record, operation, err := lockReference(ctx, ref)
	if err != nil {
		return record, err
	}
	defer operation.Close()
	record, err = stopLocked(ctx, store, record, timeout)
	if err != nil {
		return record, err
	}
	return startStopped(ctx, store, record, preflight)
}

func startStopped(ctx context.Context, store *Store, record Record, preflight StartPreflight) (Record, error) {
	ctx, cancel := context.WithTimeout(ctx, detachedStartupLimit)
	defer cancel()
	status, err := inspectUnit(ctx, record.ID, record.Generation)
	if err != nil {
		return record, err
	}
	if status.live() {
		if err := stopUnit(ctx, record.ID, record.Generation); err != nil {
			return record, err
		}
	}
	if record.RunPath != "" {
		if err := containerruntime.RecoverRun(ctx, record.RunPath); err != nil {
			return record, err
		}
	}
	if status.ActiveState == "failed" {
		if _, err := systemdCommand(ctx, "systemctl", "reset-failed", unitName(record.ID, record.Generation)); err != nil {
			return record, err
		}
	}
	cfg, err := store.Config(ctx, record.ID)
	if err != nil {
		return record, err
	}
	retained, err := store.RootFS(ctx, record.ID)
	if err != nil {
		return record, err
	}
	candidate := retained
	if info, err := os.Lstat(retained); errors.Is(err, os.ErrNotExist) {
		candidate = cfg.RootFS
		if cfg.Image != "" {
			images, err := image.OpenStore()
			if err != nil {
				return record, err
			}
			_, tree, lease, err := images.Acquire(ctx, cfg.Image)
			if err != nil {
				return record, err
			}
			defer lease.Close()
			candidate = tree
		}
	} else if err != nil {
		return record, err
	} else if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return record, errors.New("retained rootfs must be a real directory")
	}
	if _, err := rootfs.Validate(candidate); err != nil {
		return record, err
	}
	if err := rootfs.CheckUnmounted(retained); err != nil {
		return record, err
	}
	if err := rootfs.ValidateMountSources(cfg.Mounts, candidate); err != nil {
		return record, err
	}
	if preflight != nil {
		if err := preflight(ctx, cfg.Publish); err != nil {
			return record, err
		}
	}
	// The old supervisor lease fences endpoint cleanup and the generation reset.
	lease, err := store.AcquireLease(ctx, record.ID)
	if err != nil {
		return record, err
	}
	err = store.CleanupExecSocket(record.ID)
	lease.Close()
	if err != nil {
		return record, err
	}
	next, err := store.BeginExecution(ctx, record.ID, record.Generation)
	if err != nil {
		return record, err
	}
	return launchExecution(ctx, store, next)
}

// Wait pins an execution number before polling. Receipts survive later starts;
// removal returns not-found instead of redirecting a reused name.
func Wait(ctx context.Context, ref string) (int, error) {
	store, err := managementStore()
	if err != nil {
		return 125, err
	}
	record, err := store.Get(ctx, ref)
	if err != nil {
		return 125, err
	}
	id, generation := record.ID, record.Generation
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		result, finished, err := store.Completion(ctx, id, generation)
		if err != nil {
			return 125, err
		}
		if finished {
			if result.ExitCode == nil {
				return 125, errors.New("container exit status is unknown")
			}
			return *result.ExitCode, nil
		}
		latest, err := store.Get(ctx, id)
		if err != nil {
			return 125, err
		}
		if _, err := refreshRecord(ctx, store, latest, false); err != nil {
			return 125, err
		}
		select {
		case <-ctx.Done():
			return 125, ctx.Err()
		case <-ticker.C:
		}
	}
}

// Called only by the supervisor while holding its lease, before any workload.
func (store *Store) cleanupRootFSStages(id string) error {
	path := filepath.Join(store.root, id)
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".rootfs-") {
			continue
		}
		stage := filepath.Join(path, entry.Name())
		var stat unix.Stat_t
		if err := unix.Lstat(stage, &stat); err != nil {
			return err
		}
		if stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Uid != store.owner {
			return errors.New("unsafe retained rootfs staging directory")
		}
		if err := rootfs.CheckUnmounted(stage); err != nil {
			return err
		}
		if err := os.RemoveAll(stage); err != nil {
			return err
		}
	}
	return nil
}
