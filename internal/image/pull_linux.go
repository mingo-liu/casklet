//go:build linux

package image

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/mingo-liu/mini-docker/internal/rootfs"
	"golang.org/x/sys/unix"
)

func referenceKey(ref string) string { return fmt.Sprintf(".ref-%x", sha256.Sum256([]byte(ref))) }

type cachedReference struct {
	Reference string `json:"reference"`
	ID        string `json:"id"`
}

func (store *Store) readReference(key string) (cachedReference, error) {
	f, err := store.openFile(filepath.Join(store.root, key), unix.O_RDONLY)
	if err != nil {
		return cachedReference{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 1025))
	if err != nil {
		return cachedReference{}, err
	}
	var cached cachedReference
	if len(data) > 1024 || json.Unmarshal(data, &cached) != nil || ValidateID(cached.ID) != nil || referenceKey(cached.Reference) != key {
		return cachedReference{}, errors.New("invalid cached image reference")
	}
	canonical, err := NormalizeReference(cached.Reference)
	if err != nil || canonical != cached.Reference {
		return cachedReference{}, errors.New("invalid cached image reference")
	}
	return cached, nil
}

// Resolve returns the cached identity of a canonical reference, without network
// access. A removed target is a cache miss. Corrupt metadata is never ignored.
func (store *Store) Resolve(ctx context.Context, reference string) (Record, error) {
	if ValidateID(reference) == nil {
		lock, err := store.lock(ctx, true)
		if err != nil {
			return Record{}, err
		}
		defer lock.Close()
		return store.read(reference)
	}
	ref, err := NormalizeReference(reference)
	if err != nil {
		return Record{}, err
	}
	lock, err := store.lock(ctx, true)
	if err != nil {
		return Record{}, err
	}
	defer lock.Close()
	return store.resolveLocked(ref)
}

func (store *Store) resolveLocked(ref string) (Record, error) {
	cached, err := store.readReference(referenceKey(ref))
	if errors.Is(err, os.ErrNotExist) {
		return Record{}, ErrNotFound
	}
	if err != nil {
		return Record{}, err
	}
	return store.read(cached.ID)
}

// Pull refreshes a reference, selecting the native Linux platform. Download and
// extraction happen in a private transaction; publication is atomic. The store
// lock serializes pulls with imports/removal and permits interrupted staging
// recovery without deleting a live transaction.
func (store *Store) Pull(ctx context.Context, reference string) (Record, error) {
	ref, err := NormalizeReference(reference)
	if err != nil {
		return Record{}, err
	}
	parsed, err := name.ParseReference(ref)
	if err != nil {
		return Record{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	img, err := remote.Image(parsed, remote.WithContext(ctx), remote.WithPlatform(v1.Platform{OS: "linux", Architecture: runtime.GOARCH}))
	if err != nil {
		return Record{}, fmt.Errorf("pull %s: %w", ref, err)
	}
	return store.pullImage(ctx, ref, img)
}

func (store *Store) pullImage(ctx context.Context, ref string, img v1.Image) (Record, error) {
	cf, err := img.ConfigFile()
	if err != nil {
		return Record{}, err
	}
	if cf.OS != "linux" || cf.Architecture != runtime.GOARCH {
		return Record{}, fmt.Errorf("image platform %s/%s does not match linux/%s", cf.OS, cf.Architecture, runtime.GOARCH)
	}
	layers, err := img.Layers()
	if err != nil {
		return Record{}, err
	}
	if cf.RootFS.Type != "layers" || len(layers) != len(cf.RootFS.DiffIDs) || len(layers) > 256 {
		return Record{}, errors.New("invalid image layers configuration")
	}
	digest, err := img.Digest()
	if err != nil {
		return Record{}, err
	}
	launch := &LaunchConfig{Entrypoint: cf.Config.Entrypoint, Cmd: cf.Config.Cmd, Env: cf.Config.Env, Workdir: cf.Config.WorkingDir, User: cf.Config.User}
	if err := validateLaunch(launch); err != nil {
		return Record{}, err
	}
	lock, err := store.lock(ctx, false)
	if err != nil {
		return Record{}, err
	}
	defer lock.Close()
	if err := store.recoverLocked(); err != nil {
		return Record{}, err
	}
	if cached, err := store.resolveLocked(ref); err == nil && cached.ManifestDigest == digest.String() {
		if err := store.verify(ctx, cached); err != nil {
			return Record{}, err
		}
		return cached, nil
	} else if err != nil && !errors.Is(err, ErrNotFound) {
		return Record{}, err
	}
	stage, err := os.MkdirTemp(store.root, ".import-")
	if err != nil {
		return Record{}, err
	}
	defer os.RemoveAll(stage)
	tree := filepath.Join(stage, "rootfs")
	if err := os.Mkdir(tree, 0755); err != nil {
		return Record{}, err
	}
	root, err := os.OpenRoot(tree)
	if err != nil {
		return Record{}, err
	}
	defer root.Close()
	var total int64
	for i, layer := range layers {
		if cf.RootFS.DiffIDs[i].Algorithm != "sha256" {
			return Record{}, errors.New("only sha256 image layers are supported")
		}
		archive, err := os.CreateTemp(stage, ".layer-")
		if err != nil {
			return Record{}, err
		}
		count, err := unpackLayer(ctx, root, archive, layer, cf.RootFS.DiffIDs[i], maxImageBytes-total)
		closeErr := archive.Close()
		removeErr := os.Remove(archive.Name())
		if err := errors.Join(err, closeErr, removeErr); err != nil {
			return Record{}, fmt.Errorf("image layer %d: %w", i+1, err)
		}
		total += count
	}
	// Runtime must traverse the root as any configured user. Mount targets may
	// be absent in scratch/distroless images; create them in the cached tree.
	if err := setOwnership(root, ".", int(store.owner), os.Getegid()); err != nil {
		return Record{}, err
	}
	if err := root.Chmod(".", 0755); err != nil {
		return Record{}, err
	}
	for _, target := range []string{"proc", "dev", "tmp"} {
		if err := root.Mkdir(target, 0755); err != nil && !errors.Is(err, os.ErrExist) {
			return Record{}, err
		}
	}
	if _, err := rootfs.Validate(tree); err != nil {
		return Record{}, err
	}
	id, size, err := Identity(ctx, tree, runtime.GOARCH, launch)
	if err != nil {
		return Record{}, err
	}
	record, err := store.read(id)
	if err == nil {
		if err := store.verify(ctx, record); err != nil {
			return Record{}, err
		}
	} else if errors.Is(err, ErrNotFound) {
		record = Record{ID: id, Architecture: runtime.GOARCH, CreatedAt: time.Now().UTC(), SizeBytes: size, Config: launch, ManifestDigest: digest.String()}
		data, err := json.Marshal(record)
		if err != nil {
			return Record{}, err
		}
		if len(data) > 1<<20 {
			return Record{}, errors.New("image configuration exceeds 1 MiB")
		}
		if err := os.WriteFile(filepath.Join(stage, "image.json"), data, 0600); err != nil {
			return Record{}, err
		}
		lease, err := store.openFile(filepath.Join(stage, ".lease"), unix.O_RDWR|unix.O_CREAT|unix.O_EXCL)
		if err != nil {
			return Record{}, err
		}
		lease.Close()
		if err := rootfs.SyncTree(ctx, stage); err != nil {
			return Record{}, err
		}
		if err := ctx.Err(); err != nil {
			return Record{}, err
		}
		if err := os.Rename(stage, store.path(id)); err != nil {
			return Record{}, err
		}
		if err := syncDirectory(store.root); err != nil {
			return Record{}, err
		}
	} else {
		return Record{}, err
	}
	// Write the reference only after its immutable target has been synced.
	refStage, err := os.CreateTemp(store.root, ".ref-stage-")
	if err != nil {
		return Record{}, err
	}
	defer os.Remove(refStage.Name())
	writeErr := json.NewEncoder(refStage).Encode(cachedReference{Reference: ref, ID: record.ID})
	err = errors.Join(writeErr, refStage.Sync(), refStage.Close())
	if err != nil {
		return Record{}, err
	}
	if err := os.Rename(refStage.Name(), filepath.Join(store.root, referenceKey(ref))); err != nil {
		return Record{}, err
	}
	return record, syncDirectory(store.root)
}

func unpackLayer(ctx context.Context, root *os.Root, archive *os.File, layer v1.Layer, expected v1.Hash, remaining int64) (int64, error) {
	r, err := layer.Uncompressed()
	if err != nil {
		return 0, err
	}
	defer r.Close()
	limit := min(maxLayerBytes, remaining)
	digest := sha256.New()
	count, err := io.Copy(io.MultiWriter(archive, digest), io.LimitReader(contextReader{ctx: ctx, r: r}, limit+1))
	if err != nil {
		return count, err
	}
	if count > limit {
		return count, errors.New("image exceeds unpacked size limit (4 GiB per layer, 16 GiB total)")
	}
	if expected.Algorithm != "sha256" || hex.EncodeToString(digest.Sum(nil)) != expected.Hex {
		return count, errors.New("image layer DiffID mismatch")
	}
	return count, applyLayer(ctx, root, archive)
}
