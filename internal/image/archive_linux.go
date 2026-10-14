//go:build linux

package image

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/partial"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"github.com/mingo-liu/casklet/internal/rootfs"
)

type archiveBlob struct {
	path       string
	descriptor v1.Descriptor
}

func (b archiveBlob) Digest() (v1.Hash, error)            { return b.descriptor.Digest, nil }
func (b archiveBlob) Size() (int64, error)                { return b.descriptor.Size, nil }
func (b archiveBlob) MediaType() (types.MediaType, error) { return b.descriptor.MediaType, nil }
func (b archiveBlob) Compressed() (io.ReadCloser, error)  { return os.Open(b.path) }

// Load imports one native OCI layout image without contacting registries or
// modifying reference mappings/cache. Every failure leaves publication untouched.
func (store *Store) Load(ctx context.Context, source io.Reader) (Record, error) {
	stage, lease, transaction, err := store.beginImport(ctx)
	if err != nil {
		return Record{}, err
	}
	defer lease.Close()
	defer store.cleanupImport(stage, transaction)
	layoutDir := filepath.Join(stage, ".oci")
	if err := os.Mkdir(layoutDir, 0700); err != nil {
		return Record{}, err
	}
	layout, err := readOCIArchive(ctx, layoutDir, source)
	if err != nil {
		return Record{}, err
	}
	img, err := layout.nativeImage(ctx, runtime.GOARCH)
	if err != nil {
		return Record{}, err
	}
	cf := img.config
	launch := &LaunchConfig{Entrypoint: cf.Config.Entrypoint, Cmd: cf.Config.Cmd, Env: cf.Config.Env, Workdir: cf.Config.WorkingDir, User: cf.Config.User, StopSignal: cf.Config.StopSignal, Shell: cf.Config.Shell}
	launch.Healthcheck, err = imageHealthcheck(img.rawConfig)
	if err != nil {
		return Record{}, err
	}
	if err := validateLaunch(launch); err != nil {
		return Record{}, err
	}
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
	for i, d := range img.manifest.Layers {
		name, _ := layout.blobName(d)
		layer, err := partial.CompressedToLayer(archiveBlob{path: filepath.Join(layoutDir, name), descriptor: d})
		if err != nil {
			return Record{}, err
		}
		archive, err := os.CreateTemp(stage, ".layer-")
		if err != nil {
			return Record{}, err
		}
		count, unpackErr := unpackLayer(ctx, root, archive, layer, cf.RootFS.DiffIDs[i], maxImageBytes-total)
		if unpackErr == nil {
			unpackErr = validateOCILayerArchive(ctx, archive)
		}
		if err := errors.Join(unpackErr, archive.Close(), os.Remove(archive.Name())); err != nil {
			return Record{}, fmt.Errorf("OCI image layer %d: %w", i+1, err)
		}
		total += count
	}
	// Same normalization and validation as registry pull, including account access.
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
	if err := os.RemoveAll(layoutDir); err != nil {
		return Record{}, err
	}
	return store.publish(ctx, stage, Record{ID: id, Architecture: runtime.GOARCH, CreatedAt: time.Now().UTC(), SizeBytes: size, Config: launch, ManifestDigest: img.digest}, "")
}

type boundedImageWriter struct {
	ctx    context.Context
	writer io.Writer
	count  int64
	limit  int64
}

func (w *boundedImageWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	if int64(len(p)) > w.limit-w.count {
		return 0, errors.New("OCI export exceeds 4 GiB flattened layer limit")
	}
	n, err := w.writer.Write(p)
	w.count += int64(n)
	return n, err
}

// Save synthesizes a standard OCI layout from the immutable flattened tree.
// A shared image lease remains held through output; no metadata lock covers I/O.
func (store *Store) Save(ctx context.Context, id string, destination io.Writer) error {
	record, tree, imageLease, err := store.Acquire(ctx, id)
	if err != nil {
		return err
	}
	defer imageLease.Close()
	if record.Config == nil {
		return errors.New("image save requires an OCI image; directory imports have a different ownership and capability policy")
	}
	stage, lease, transaction, err := store.beginImport(ctx)
	if err != nil {
		return err
	}
	defer lease.Close()
	defer store.cleanupImport(stage, transaction)
	layer, err := os.CreateTemp(stage, ".export-layer-")
	if err != nil {
		return err
	}
	defer layer.Close()
	digest := sha256.New()
	counter := &boundedImageWriter{ctx: ctx, writer: io.MultiWriter(layer, digest), limit: maxLayerBytes}
	if err := writeImageLayer(ctx, tree, counter); err != nil {
		return err
	}
	layerHash := v1.Hash{Algorithm: "sha256", Hex: hex.EncodeToString(digest.Sum(nil))}
	cfg := record.Config
	// The extension includes StartInterval, which the pinned registry Config omits.
	launch := map[string]any{"Entrypoint": cfg.Entrypoint, "Cmd": cfg.Cmd, "Env": cfg.Env, "WorkingDir": cfg.Workdir, "User": cfg.User, "StopSignal": cfg.StopSignal, "Shell": cfg.Shell}
	if h := cfg.Healthcheck; h != nil {
		health := map[string]any{"Test": h.Test}
		for key, value := range map[string]any{"Interval": h.Interval, "Timeout": h.Timeout, "StartPeriod": h.StartPeriod, "StartInterval": h.StartInterval, "Retries": h.Retries} {
			health[key] = value
		}
		launch["Healthcheck"] = health
	}
	configBytes, err := json.Marshal(map[string]any{"architecture": record.Architecture, "os": "linux", "config": launch, "rootfs": v1.RootFS{Type: "layers", DiffIDs: []v1.Hash{layerHash}}})
	if err != nil {
		return err
	}
	descriptor := func(raw []byte, media types.MediaType) v1.Descriptor {
		hash := sha256.Sum256(raw)
		return v1.Descriptor{MediaType: media, Size: int64(len(raw)), Digest: v1.Hash{Algorithm: "sha256", Hex: hex.EncodeToString(hash[:])}}
	}
	configDesc := descriptor(configBytes, types.OCIConfigJSON)
	manifest := v1.Manifest{SchemaVersion: 2, MediaType: types.OCIManifestSchema1, Config: configDesc, Layers: []v1.Descriptor{{MediaType: types.OCIUncompressedLayer, Size: counter.count, Digest: layerHash}}}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	manifestDesc := descriptor(manifestBytes, types.OCIManifestSchema1)
	manifestDesc.Platform = &v1.Platform{OS: "linux", Architecture: record.Architecture}
	indexBytes, err := json.Marshal(v1.IndexManifest{SchemaVersion: 2, MediaType: types.OCIImageIndex, Manifests: []v1.Descriptor{manifestDesc}})
	if err != nil {
		return err
	}
	if _, err := layer.Seek(0, io.SeekStart); err != nil {
		return err
	}
	writer := tar.NewWriter(&boundedImageWriter{ctx: ctx, writer: destination, limit: maxOCIArchiveBytes})
	files := []struct {
		name string
		data []byte
	}{{"oci-layout", []byte(`{"imageLayoutVersion":"1.0.0"}`)}, {"index.json", indexBytes}, {"blobs/sha256/" + configDesc.Digest.Hex, configBytes}, {"blobs/sha256/" + manifestDesc.Digest.Hex, manifestBytes}}
	for _, file := range files {
		if err := writer.WriteHeader(&tar.Header{Name: file.name, Mode: 0600, Typeflag: tar.TypeReg, Size: int64(len(file.data))}); err != nil {
			return err
		}
		if _, err := writer.Write(file.data); err != nil {
			return err
		}
	}
	if err := writer.WriteHeader(&tar.Header{Name: "blobs/sha256/" + layerHash.Hex, Mode: 0600, Typeflag: tar.TypeReg, Size: counter.count}); err != nil {
		return err
	}
	if _, err := io.Copy(writer, contextReader{ctx: ctx, r: layer}); err != nil {
		return err
	}
	return writer.Close()
}
func writeImageLayer(ctx context.Context, tree string, destination io.Writer) error {
	root, err := os.OpenRoot(tree)
	if err != nil {
		return err
	}
	defer root.Close()
	writer := tar.NewWriter(destination)
	count := 0
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		count++
		if count > maxLayerEntries {
			return errors.New("OCI export exceeds layer entry limit")
		}
		info, err := root.Lstat(name)
		if err != nil {
			return err
		}
		target := ""
		if info.Mode()&os.ModeSymlink != 0 {
			target, err = root.Readlink(name)
			if err != nil {
				return err
			}
		}
		h, err := tar.FileInfoHeader(info, target)
		if err != nil {
			return err
		}
		h.Name = name
		h.Uname, h.Gname = "", ""
		uid, gid := rootfs.Ownership(info)
		h.Uid, h.Gid = int(uid), int(gid)
		if err := writer.WriteHeader(h); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		f, err := root.Open(name)
		if err != nil {
			return err
		}
		_, copyErr := io.CopyN(writer, contextReader{ctx: ctx, r: f}, h.Size)
		return errors.Join(copyErr, f.Close())
	})
	if err != nil {
		return err
	}
	return writer.Close()
}
