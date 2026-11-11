//go:build linux

package image

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/partial"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"github.com/mingo-liu/casklet/internal/rootfs"
	"golang.org/x/sys/unix"
)

const _blobDownloadWorkers = 3

// Blob locks are stable files: shared locks pin reusable data through extraction,
// and an exclusive lock covers download, publication and removal. Never
// unlink a lock file, including during prune, or waiters could use another inode.
type cachedBlob struct {
	file       *os.File
	lease      *os.File
	descriptor v1.Descriptor
}

var (
	_ partial.CompressedLayer = (*cachedBlob)(nil)
	_ io.Closer               = (*cachedBlob)(nil)
)

var errCorruptBlob = errors.New("cached compressed layer does not match its digest and size")

func (blob *cachedBlob) Close() error { return errors.Join(blob.file.Close(), blob.lease.Close()) }

func (blob *cachedBlob) Digest() (v1.Hash, error) { return blob.descriptor.Digest, nil }

func (blob *cachedBlob) Size() (int64, error) { return blob.descriptor.Size, nil }

func (blob *cachedBlob) MediaType() (types.MediaType, error) { return blob.descriptor.MediaType, nil }

func (blob *cachedBlob) Compressed() (io.ReadCloser, error) {
	// SectionReader uses ReadAt, so repeated manifest layers share one lease
	// without sharing offsets or locking themselves out of the cache.
	return io.NopCloser(io.NewSectionReader(blob.file, 0, blob.descriptor.Size)), nil
}

func (store *Store) blobDirectory(create bool) (string, error) {
	if err := store.checkDirectory(store.root, true); err != nil {
		return "", err
	}
	dir := filepath.Join(store.root, ".blobs")
	if create {
		if err := os.Mkdir(dir, 0700); err == nil {
			if err := syncDirectory(store.root); err != nil {
				return "", err
			}
		} else if !errors.Is(err, os.ErrExist) {
			return "", err
		}
	}
	if err := store.checkDirectory(dir, true); err != nil {
		return "", err
	}
	if err := rootfs.CheckUnmounted(dir); err != nil {
		return "", err
	}
	return dir, nil
}

func (store *Store) blobLock(ctx context.Context, dir, digest string, shared bool) (*os.File, error) {
	file, err := store.openFile(filepath.Join(dir, ".lock-"+digest), unix.O_RDWR|unix.O_CREAT)
	if err != nil {
		return nil, err
	}
	return waitFileLock(ctx, file, shared)
}

// Open and validate cache data under its stable lock. Unsafe files fail rather
// than being replaced. Corrupt regular files also fail explicitly; prune can
// reclaim them before a later pull redownloads verified data.
func (store *Store) openVerifiedBlob(ctx context.Context, dir string, d v1.Descriptor) (*os.File, error) {
	file, err := store.openFile(filepath.Join(dir, d.Digest.Hex), unix.O_RDONLY)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*os.File, error) { _ = file.Close(); return nil, err }
	info, err := file.Stat()
	if err != nil {
		return fail(err)
	}
	if info.Size() != d.Size {
		return fail(errCorruptBlob)
	}
	hash := sha256.New()
	count, err := io.Copy(hash, io.LimitReader(contextReader{ctx: ctx, r: file}, d.Size+1))
	if err != nil {
		return fail(err)
	}
	if count != d.Size || hex.EncodeToString(hash.Sum(nil)) != d.Digest.Hex {
		return fail(errCorruptBlob)
	}
	return file, nil
}

func (store *Store) acquireBlob(ctx context.Context, dir string, layer v1.Layer, d v1.Descriptor, event Progress) (*cachedBlob, error) {
	if err := validateBlobDescriptor(d); err != nil {
		return nil, err
	}
	for {
		lease, err := store.blobLock(ctx, dir, d.Digest.Hex, true)
		if err != nil {
			return nil, err
		}
		file, err := store.openVerifiedBlob(ctx, dir, d)
		if err == nil {
			if err := touchCachedBlob(file); err != nil {
				_ = file.Close()
				_ = lease.Close()
				return nil, err
			}
			event.Stage, event.Current = ProgressLayerCached, 0
			reportProgress(ctx, event)
			return &cachedBlob{file: file, lease: lease, descriptor: d}, nil
		}
		_ = lease.Close()
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		// A miss does not wait indefinitely for EX: another pull may publish
		// and downgrade to SH before this one locks. Waiting for EX then would
		// deadlock if both pulls pin different layers while filling the rest.
		lease, err = store.openFile(filepath.Join(dir, ".lock-"+d.Digest.Hex), unix.O_RDWR|unix.O_CREAT)
		if err != nil {
			return nil, err
		}
		err = unix.Flock(int(lease.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EINTR) {
			_ = lease.Close()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(10 * time.Millisecond):
			}
			continue
		}
		if err != nil {
			_ = lease.Close()
			return nil, err
		}
		return store.fillBlobLocked(ctx, dir, layer, d, event, lease)
	}
}

func (store *Store) fillBlobLocked(ctx context.Context, dir string, layer v1.Layer, d v1.Descriptor, event Progress, lease *os.File) (blob *cachedBlob, resultErr error) {
	defer func() {
		if resultErr != nil {
			_ = lease.Close()
		}
	}()
	file, err := store.openVerifiedBlob(ctx, dir, d)
	if err == nil {
		if err := touchCachedBlob(file); err != nil {
			_ = file.Close()
			return nil, err
		}
		if _, err := waitFileLock(ctx, lease, true); err != nil {
			_ = file.Close()
			return nil, err
		}
		event.Stage, event.Current = ProgressLayerCached, 0
		reportProgress(ctx, event)
		return &cachedBlob{file: file, lease: lease, descriptor: d}, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := store.recoverBlobStagesLocked(dir, d.Digest.Hex); err != nil {
		return nil, err
	}
	file, err = store.downloadBlob(ctx, dir, layer, d, event)
	if err != nil {
		return nil, err
	}
	if _, err := waitFileLock(ctx, lease, true); err != nil {
		_ = file.Close()
		return nil, err
	}
	return &cachedBlob{file: file, lease: lease, descriptor: d}, nil
}

func (store *Store) recoverBlobStagesLocked(dir, digest string, dryRun ...bool) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".stage-"+digest+"-") {
			continue
		}
		var (
			path      = filepath.Join(dir, entry.Name())
			file, err = store.openFile(path, unix.O_RDONLY)
		)
		if err != nil {
			return err
		}
		_ = file.Close()
		if len(dryRun) == 0 || !dryRun[0] {
			if err := os.Remove(path); err != nil {
				return err
			}
		}
	}
	return nil
}

func (store *Store) downloadBlob(ctx context.Context, dir string, layer v1.Layer, d v1.Descriptor, event Progress) (file *os.File, resultErr error) {
	event.Stage, event.Current = ProgressDownloading, 0
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
	input, err := layer.Compressed()
	if err != nil {
		return nil, err
	}
	var (
		closeOnce sync.Once
		closeErr  error
	)
	closeInput := func() { closeOnce.Do(func() { closeErr = input.Close() }) }
	stopClose := context.AfterFunc(ctx, closeInput)
	defer func() { stopClose(); closeInput() }()
	stage, err := os.CreateTemp(dir, ".stage-"+d.Digest.Hex+"-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = stage.Close() }()
	defer func() { _ = os.Remove(stage.Name()) }()
	hash := sha256.New()
	count, err := io.Copy(io.MultiWriter(stage, hash), io.LimitReader(contextReader{ctx: ctx, r: countingReader{reader: input, read: progress.add}}, d.Size+1))
	closeInput()
	if err := errors.Join(err, closeErr); err != nil {
		return nil, errors.Join(ctx.Err(), err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if count != d.Size {
		return nil, errors.New("compressed image layer size does not match its manifest")
	}
	if hex.EncodeToString(hash.Sum(nil)) != d.Digest.Hex {
		return nil, errors.New("compressed image layer digest does not match its manifest")
	}
	if err := stage.Sync(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.Rename(stage.Name(), filepath.Join(dir, d.Digest.Hex)); err != nil {
		return nil, err
	}
	if err := syncDirectory(dir); err != nil {
		return nil, err
	}
	// Return a new no-follow validated descriptor instead of a writeable staging
	// handle. The digest and size were verified on the bytes just published.
	file, err = store.openFile(filepath.Join(dir, d.Digest.Hex), unix.O_RDONLY)
	if err != nil {
		return nil, err
	}
	progress.finish(ProgressDownloaded)
	return file, nil
}

// downloadBlobs deduplicates repeated layers before scheduling bounded workers.
// It joins every worker before returning, including on failure. cancelRemote
// cancels remote.Image's construction context too, interrupting pending HTTP
// headers in Layer.Compressed before a stream exists to close.
func (store *Store) downloadBlobs(ctx context.Context, layers []v1.Layer, descriptors []v1.Descriptor, ref string, cancelRemote context.CancelFunc) ([]*cachedBlob, error) {
	if len(layers) != len(descriptors) || len(layers) > 256 {
		return nil, errors.New("invalid image manifest layer count")
	}
	var compressed int64
	for i, d := range descriptors {
		if err := validateBlobDescriptor(d); err != nil {
			return nil, fmt.Errorf("image layer %d: %w", i+1, err)
		}
		if compressed > _maxImageBytes-d.Size {
			return nil, errors.New("image exceeds compressed size limit (16 GiB)")
		}
		compressed += d.Size
	}
	dir, err := store.blobDirectory(true)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type job struct {
		index   int
		indexes []int
	}
	var (
		jobs     = make([]job, 0, len(layers))
		byDigest = make(map[string]int)
	)
	for i, d := range descriptors {
		if j, ok := byDigest[d.Digest.Hex]; ok {
			prior := descriptors[jobs[j].index]
			if prior.Size != d.Size || prior.MediaType != d.MediaType {
				return nil, errors.New("repeated image layer has inconsistent descriptors")
			}
			jobs[j].indexes = append(jobs[j].indexes, i)
		} else {
			byDigest[d.Digest.Hex] = len(jobs)
			jobs = append(jobs, job{index: i, indexes: []int{i}})
		}
	}
	result := make([]*cachedBlob, len(layers))
	// Every validated unique layer fits; preloading completes before workers start.
	queue := make(chan job, len(jobs))
	for _, j := range jobs {
		queue <- j
	}
	close(queue)
	var (
		workers     sync.WaitGroup
		failureOnce sync.Once
		failure     error
	)
	for range min(_blobDownloadWorkers, len(jobs)) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for j := range queue {
				if ctx.Err() != nil {
					return
				}
				var (
					d         = descriptors[j.index]
					event     = Progress{Reference: ref, Layer: d.Digest.String(), Index: j.index + 1, Layers: len(layers), Total: d.Size}
					blob, err = store.acquireBlob(ctx, dir, layers[j.index], d, event)
				)
				if err != nil {
					failureOnce.Do(func() {
						failure = fmt.Errorf("image layer %d: %w", j.index+1, err)
						cancel()
						if cancelRemote != nil {
							cancelRemote()
						}
					})
					return
				}
				for _, i := range j.indexes {
					result[i] = blob
				}
			}
		}()
	}
	workers.Wait()
	if failure == nil {
		failure = ctx.Err()
	}
	if failure != nil {
		closeBlobs(result)
		return nil, failure
	}
	return result, nil
}

// pruneBlobs is independent of the global image lock and skips every digest
// currently being downloaded, validated or extracted. Immutable image roots
// remain usable after compressed cache data is reclaimed.
func (store *Store) pruneBlobs(ctx context.Context, dryRun bool) error {
	_, err := store.PruneCache(ctx, dryRun)
	return err
}

func validateBlobDescriptor(d v1.Descriptor) error {
	if d.Digest.Algorithm != "sha256" || ValidateID(d.Digest.String()) != nil {
		return errors.New("only valid sha256 image layer digests are supported")
	}
	if d.Size < 0 || d.Size > _maxLayerBytes {
		return errors.New("image exceeds compressed layer size limit (4 GiB)")
	}
	return nil
}

func closeBlobs(blobs []*cachedBlob) {
	seen := make(map[*cachedBlob]bool, len(blobs))
	for _, blob := range blobs {
		if blob != nil && !seen[blob] {
			seen[blob] = true
			_ = blob.Close()
		}
	}
}
