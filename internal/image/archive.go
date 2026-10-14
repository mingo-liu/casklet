package image

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

const maxOCIArchiveBytes int64 = 32 << 30
const maxOCIArchiveEntries = 4096
const maxOCIMetadataBytes int64 = 1 << 20

// OCI archives are staged as data, never extracted as filesystem trees. Only
// canonical sha256 blobs and bounded metadata are passed to the image decoder.
type ociArchive struct {
	dir   string
	sizes map[string]int64
}
type ociStream struct {
	ctx    context.Context
	reader io.Reader
	count  int64
	tail   [1024]byte
}

func (r *ociStream) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	if r.count >= maxOCIArchiveBytes {
		return 0, errors.New("OCI archive exceeds 32 GiB")
	}
	if int64(len(p)) > maxOCIArchiveBytes-r.count {
		p = p[:maxOCIArchiveBytes-r.count]
	}
	n, err := r.reader.Read(p)
	if n >= len(r.tail) {
		copy(r.tail[:], p[n-len(r.tail):n])
	} else {
		copy(r.tail[:], r.tail[n:])
		copy(r.tail[len(r.tail)-n:], p[:n])
	}
	r.count += int64(n)
	return n, err
}
func readOCIArchive(ctx context.Context, dir string, source io.Reader) (*ociArchive, error) {
	a := &ociArchive{dir: dir, sizes: map[string]int64{}}
	stream := &ociStream{ctx: ctx, reader: source}
	reader := tar.NewReader(stream)
	seen := map[string]bool{}
	for count := 0; ; count++ {
		before := stream.count
		h, err := reader.Next()
		if err == io.EOF {
			if err := finishOCIStream(stream, before); err != nil {
				return nil, err
			}
			break
		}
		if err != nil {
			return nil, err
		}
		if count >= maxOCIArchiveEntries {
			return nil, errors.New("OCI archive exceeds 4096 entries")
		}
		for key := range h.PAXRecords {
			if strings.HasPrefix(key, "GNU.sparse.") {
				return nil, errors.New("sparse OCI archive entries are unsupported")
			}
		}
		name, e := archivePath(h.Name)
		if e != nil {
			return nil, e
		}
		if seen[name] {
			return nil, errors.New("OCI archive contains duplicate paths")
		}
		seen[name] = true
		if h.Typeflag == tar.TypeDir {
			if strings.HasPrefix(name, "blobs/") && name != "blobs/sha256" {
				return nil, fmt.Errorf("unsupported OCI blob directory %q", name)
			}
			if h.Size != 0 {
				return nil, errors.New("OCI archive directory has content")
			}
			continue
		}
		if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA {
			return nil, errors.New("OCI archive allows only regular files and layout directories")
		}
		if h.Size < 0 || h.Size > maxLayerBytes {
			return nil, errors.New("OCI blob exceeds 4 GiB")
		}
		blob := strings.TrimPrefix(name, "blobs/sha256/")
		isBlob := strings.HasPrefix(name, "blobs/sha256/") && ValidateID("sha256:"+blob) == nil
		// OCI permits extra layout files; discard bounded unknown regular data, but
		// never follow them or use Docker manifest.json to infer an image.
		if !isBlob && (strings.HasPrefix(name, "blobs/") || name == "." || h.Size > maxOCIMetadataBytes) {
			return nil, fmt.Errorf("unsupported OCI archive path %q", name)
		}
		if !isBlob && name != "oci-layout" && name != "index.json" {
			if _, err := io.Copy(io.Discard, reader); err != nil {
				return nil, err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0700); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return nil, err
		}
		digest := sha256.New()
		_, copyErr := io.Copy(io.MultiWriter(f, digest), reader)
		if err := errors.Join(copyErr, f.Close()); err != nil {
			return nil, err
		}
		if isBlob && hex.EncodeToString(digest.Sum(nil)) != blob {
			return nil, fmt.Errorf("OCI blob digest mismatch: %s", name)
		}
		a.sizes[name] = h.Size
	}
	var marker struct {
		Version string `json:"imageLayoutVersion"`
	}
	if err := a.decodeFile("oci-layout", &marker); err != nil {
		return nil, err
	}
	if marker.Version != "1.0.0" {
		return nil, errors.New("unsupported OCI image layout version")
	}
	return a, nil
}
func (a *ociArchive) decodeFile(name string, target any) error {
	size, ok := a.sizes[name]
	if !ok || size > maxOCIMetadataBytes {
		return fmt.Errorf("missing or oversized OCI metadata: %s", name)
	}
	raw, err := os.ReadFile(filepath.Join(a.dir, name))
	if err != nil {
		return err
	}
	if err := validateOCIJSON(raw); err != nil {
		return fmt.Errorf("invalid OCI metadata %s: %w", name, err)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("invalid OCI metadata %s: %w", name, err)
	}
	return nil
}
func (a *ociArchive) blobName(d v1.Descriptor) (string, error) {
	if d.Digest.Algorithm != "sha256" || ValidateID(d.Digest.String()) != nil || d.Size < 0 || d.Size > maxLayerBytes || len(d.URLs) > 0 || len(d.Data) > 0 {
		return "", errors.New("OCI descriptors require local sha256 blobs with valid sizes")
	}
	name := path.Join("blobs/sha256", d.Digest.Hex)
	size, ok := a.sizes[name]
	if !ok || size != d.Size {
		return "", fmt.Errorf("missing OCI blob or descriptor size mismatch: %s", d.Digest.String())
	}
	return name, nil
}

type archiveImage struct {
	manifest  v1.Manifest
	config    v1.ConfigFile
	rawConfig []byte
	digest    string
}

func (a *ociArchive) nativeImage(ctx context.Context, architecture string) (archiveImage, error) {
	var index v1.IndexManifest
	if err := a.decodeFile("index.json", &index); err != nil {
		return archiveImage{}, err
	}
	candidates := map[string]archiveImage{}
	visited := 0
	var visit func(v1.IndexManifest, int) error
	visit = func(index v1.IndexManifest, depth int) error {
		if index.SchemaVersion != 2 || len(index.Manifests) == 0 || len(index.Manifests) > 256 || depth > 4 {
			return errors.New("invalid OCI index or excessive nesting")
		}
		for _, d := range index.Manifests {
			if err := ctx.Err(); err != nil {
				return err
			}
			visited++
			if visited > 1024 {
				return errors.New("OCI archive exceeds descriptor limit")
			}
			name, err := a.blobName(d)
			if err != nil {
				return err
			}
			if d.Platform != nil && (d.Platform.OS != "linux" || d.Platform.Architecture != architecture) {
				continue
			}
			switch d.MediaType {
			case types.OCIImageIndex, types.DockerManifestList:
				var nested v1.IndexManifest
				if err := a.decodeFile(name, &nested); err != nil {
					return err
				}
				if err := visit(nested, depth+1); err != nil {
					return err
				}
			case types.OCIManifestSchema1, types.DockerManifestSchema2:
				var image archiveImage
				if err := a.decodeFile(name, &image.manifest); err != nil {
					return err
				}
				m := image.manifest
				if m.SchemaVersion != 2 || m.ArtifactType != "" || len(m.Layers) > 256 {
					return errors.New("invalid OCI image manifest")
				}
				if m.Config.MediaType != types.OCIConfigJSON && m.Config.MediaType != types.DockerConfigJSON {
					return errors.New("unsupported OCI image config media type")
				}
				configName, err := a.blobName(m.Config)
				if err != nil {
					return err
				}
				if err := a.decodeFile(configName, &image.config); err != nil {
					return err
				}
				image.rawConfig, err = os.ReadFile(filepath.Join(a.dir, configName))
				if err != nil {
					return err
				}
				if image.config.OS != "linux" || image.config.Architecture != architecture {
					if d.Platform != nil {
						return errors.New("OCI descriptor and config platforms disagree")
					}
					continue
				}
				if image.config.RootFS.Type != "layers" || len(image.config.RootFS.DiffIDs) != len(m.Layers) {
					return errors.New("invalid OCI image layers configuration")
				}
				for i, layer := range m.Layers {
					if _, err := a.blobName(layer); err != nil {
						return err
					}
					switch layer.MediaType {
					case types.OCILayer, types.OCILayerZStd, types.OCIUncompressedLayer, types.DockerLayer, types.DockerUncompressedLayer:
					default:
						return errors.New("unsupported OCI layer media type")
					}
					if ValidateID(image.config.RootFS.DiffIDs[i].String()) != nil {
						return errors.New("invalid OCI layer DiffID")
					}
				}
				image.digest = d.Digest.String()
				candidates[image.digest] = image
			default:
				return errors.New("unsupported OCI index descriptor media type")
			}
		}
		return nil
	}
	if err := visit(index, 0); err != nil {
		return archiveImage{}, err
	}
	if len(candidates) != 1 {
		return archiveImage{}, fmt.Errorf("OCI archive must contain exactly one distinct linux/%s image (found %d)", architecture, len(candidates))
	}
	for _, image := range candidates {
		return image, nil
	}
	panic("unreachable")
}

func finishOCIStream(stream *ociStream, before int64) error {
	if stream.count-before != (512-before%512)%512+1024 {
		return errors.New("OCI archive is missing its tar terminator")
	}
	for _, b := range stream.tail {
		if b != 0 {
			return errors.New("invalid OCI tar terminator")
		}
	}
	buffer := make([]byte, 32<<10)
	for {
		n, err := stream.Read(buffer)
		for _, b := range buffer[:n] {
			if b != 0 {
				return errors.New("nonzero trailing OCI archive data")
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// Check layer framing as well as DiffID: a matching digest does not make a
// truncated tar or a concatenated archive valid filesystem input.
func validateOCILayerArchive(ctx context.Context, archive *os.File) error {
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return err
	}
	stream := &ociStream{ctx: ctx, reader: archive}
	reader := tar.NewReader(stream)
	for count := 0; ; count++ {
		before := stream.count
		header, err := reader.Next()
		if err == io.EOF {
			return finishOCIStream(stream, before)
		}
		if err != nil {
			return err
		}
		if err := rejectSparseLayer(header); err != nil {
			return err
		}
		if count >= maxLayerEntries {
			return errors.New("OCI layer exceeds entry limit")
		}
		if _, err := io.Copy(io.Discard, reader); err != nil {
			return err
		}
	}
}

// OCI metadata can contain extensions, but duplicate keys and excessively deep
// JSON are rejected instead of allowing ambiguous security/identity fields.
func validateOCIJSON(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var value func(int) error
	value = func(depth int) error {
		if depth > 64 {
			return errors.New("OCI JSON exceeds nesting limit")
		}
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := map[string]bool{}
			for decoder.More() {
				key, err := decoder.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok {
					return errors.New("invalid OCI JSON object")
				}
				if seen[name] {
					return errors.New("OCI JSON contains duplicate fields")
				}
				seen[name] = true
				if err := value(depth + 1); err != nil {
					return err
				}
			}
		case '[':
			for decoder.More() {
				if err := value(depth + 1); err != nil {
					return err
				}
			}
		default:
			return errors.New("invalid OCI JSON delimiter")
		}
		_, err = decoder.Token()
		return err
	}
	if err := value(0); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("trailing OCI JSON data")
	}
	return nil
}
