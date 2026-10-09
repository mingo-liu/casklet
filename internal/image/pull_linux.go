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
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/partial"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/mingo-liu/casklet/internal/rootfs"
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
func (store *Store) Resolve(ctx context.Context, reference string) (record Record, err error) {
	defer func() {
		if err == nil {
			reportProgress(ctx, Progress{Stage: ProgressCached, Reference: reference})
		}
	}()
	if IsIDReference(reference) {
		lock, err := store.lock(ctx, true)
		if err != nil {
			return Record{}, err
		}
		defer lock.Close()
		// Prefer an exact cached name to a bare ID prefix, as with containers.
		if !strings.HasPrefix(reference, "sha256:") {
			ref, err := NormalizeReference(reference)
			if err != nil {
				return Record{}, err
			}
			if record, err := store.resolveLocked(ref); !errors.Is(err, ErrNotFound) {
				return record, err
			}
		}
		return store.resolveIDLocked(ctx, reference)
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
	reportProgress(ctx, Progress{Stage: ProgressResolving, Reference: ref})
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
	manifest, err := img.Manifest()
	if err != nil {
		return Record{}, err
	}
	if len(manifest.Layers) != len(layers) {
		return Record{}, errors.New("invalid image manifest layer count")
	}
	digest, err := img.Digest()
	if err != nil {
		return Record{}, err
	}
	launch := &LaunchConfig{Shell: cf.Config.Shell, Entrypoint: cf.Config.Entrypoint, Cmd: cf.Config.Cmd, Env: cf.Config.Env, Workdir: cf.Config.WorkingDir, User: cf.Config.User, StopSignal: cf.Config.StopSignal}
	rawConfig, err := img.RawConfigFile()
	if err != nil {
		return Record{}, err
	}
	launch.Healthcheck, err = imageHealthcheck(rawConfig)
	if err != nil {
		return Record{}, err
	}
	if err := validateLaunch(launch); err != nil {
		return Record{}, err
	}
	reportProgress(ctx, Progress{Stage: ProgressWaiting, Reference: ref})
	lock, err := store.lock(ctx, false)
	if err != nil {
		return Record{}, err
	}
	defer lock.Close()
	if err := store.recoverLocked(); err != nil {
		return Record{}, err
	}
	if cached, err := store.resolveLocked(ref); err == nil && cached.ManifestDigest == digest.String() && sameLaunchConfig(cached.Config, launch) {
		reportProgress(ctx, Progress{Stage: ProgressVerifyingImage, Reference: ref})
		if err := store.verify(ctx, cached); err != nil {
			return Record{}, err
		}
		reportProgress(ctx, Progress{Stage: ProgressUpToDate, Reference: ref})
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
		event := Progress{Reference: ref, Layer: manifest.Layers[i].Digest.String(), Index: i + 1, Layers: len(layers), Total: manifest.Layers[i].Size}
		count, err := unpackLayer(ctx, root, archive, layer, cf.RootFS.DiffIDs[i], maxImageBytes-total, event)
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
	reportProgress(ctx, Progress{Stage: ProgressVerifyingImage, Reference: ref})
	id, size, err := Identity(ctx, tree, runtime.GOARCH, launch)
	if err != nil {
		return Record{}, err
	}
	reportProgress(ctx, Progress{Stage: ProgressPublishing, Reference: ref})
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
	if err := syncDirectory(store.root); err != nil {
		return Record{}, err
	}
	reportProgress(ctx, Progress{Stage: ProgressReady, Reference: ref})
	return record, nil
}

// trackedLayer preserves go-containerregistry's decompression and blob digest
// verification while counting bytes read from the compressed network stream.
type trackedLayer struct {
	v1.Layer
	progress *streamProgress
}

func (l trackedLayer) Compressed() (io.ReadCloser, error) {
	r, err := l.Layer.Compressed()
	if err != nil {
		return nil, err
	}
	return struct {
		io.Reader
		io.Closer
	}{countingReader{reader: r, read: l.progress.add}, r}, nil
}

func unpackLayer(ctx context.Context, root *os.Root, archive *os.File, layer v1.Layer, expected v1.Hash, remaining int64, events ...Progress) (count int64, resultErr error) {
	event := Progress{Stage: ProgressDownloading}
	if len(events) > 0 {
		event = events[0]
		event.Stage = ProgressDownloading
	}
	progress := &streamProgress{ctx: ctx, event: event, now: time.Now}
	reportProgress(ctx, event)
	defer func() {
		if resultErr != nil {
			stage := ProgressFailed
			if ctx.Err() != nil {
				stage = ProgressCanceled
			}
			progress.finish(stage)
		}
	}()
	tracked, err := partial.CompressedToLayer(trackedLayer{Layer: layer, progress: progress})
	if err != nil {
		return 0, err
	}
	r, err := tracked.Uncompressed()
	if err != nil {
		return 0, err
	}
	defer r.Close()
	limit := min(maxLayerBytes, remaining)
	digest := sha256.New()
	count, err = io.Copy(io.MultiWriter(archive, digest), io.LimitReader(contextReader{ctx: ctx, r: r}, limit+1))
	if err != nil {
		return count, err
	}
	if count > limit {
		return count, errors.New("image exceeds unpacked size limit (4 GiB per layer, 16 GiB total)")
	}
	progress.finish(ProgressDownloaded)
	progress.finish(ProgressVerifying)
	if expected.Algorithm != "sha256" || hex.EncodeToString(digest.Sum(nil)) != expected.Hex {
		return count, errors.New("image layer DiffID mismatch")
	}
	progress.event.Stage = ProgressExtracting
	progress.event.Current, progress.event.Total = 0, 2*count
	progress.last = time.Time{}
	reportProgress(ctx, progress.event)
	if err := applyLayer(ctx, root, archive, progress.add); err != nil {
		return count, err
	}
	progress.event.Current = progress.event.Total
	progress.finish(ProgressLayerComplete)
	return count, nil
}
