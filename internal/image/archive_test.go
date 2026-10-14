package image

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

// The fixture is written by the pinned third-party OCI layout implementation,
// independently of Save. It includes ordered whiteouts and complete defaults.
func archiveFixture(t *testing.T) []byte {
	t.Helper()
	header := make([]byte, 64)
	copy(header, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
	binary.LittleEndian.PutUint16(header[16:], uint16(elf.ET_EXEC))
	machine := elf.EM_AARCH64
	if runtime.GOARCH == "amd64" {
		machine = elf.EM_X86_64
	}
	binary.LittleEndian.PutUint16(header[18:], uint16(machine))
	binary.LittleEndian.PutUint32(header[20:], 1)
	binary.LittleEndian.PutUint16(header[52:], 64)
	uid, gid := os.Geteuid(), os.Getegid()
	if uid == 0 {
		uid, gid = 1000, 1001
	}
	var layers []v1.Layer
	for _, files := range [][]struct {
		h    tar.Header
		data []byte
	}{
		{{tar.Header{Name: "bin/toolbox", Typeflag: tar.TypeReg, Mode: 0755}, header}, {tar.Header{Name: "obsolete", Typeflag: tar.TypeReg, Mode: 0644}, []byte("old")}},
		{{tar.Header{Name: ".wh.obsolete", Typeflag: tar.TypeReg, Mode: 0644}, nil}, {tar.Header{Name: "value", Typeflag: tar.TypeReg, Mode: 0640, Uid: uid, Gid: gid}, []byte("exact\x00bytes")}, {tar.Header{Name: "sym", Typeflag: tar.TypeSymlink, Linkname: "value", Mode: 0777, Uid: uid, Gid: gid}, nil}},
	} {
		var buf bytes.Buffer
		w := tar.NewWriter(&buf)
		for _, f := range files {
			if os.Geteuid() != 0 && f.h.Uid == 0 {
				f.h.Uid, f.h.Gid = os.Geteuid(), os.Getegid()
			}
			f.h.Size = int64(len(f.data))
			if err := w.WriteHeader(&f.h); err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write(f.data); err != nil {
				t.Fatal(err)
			}
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		layer, err := tarball.LayerFromReader(bytes.NewReader(buf.Bytes()), tarball.WithMediaType(types.OCILayer))
		if err != nil {
			t.Fatal(err)
		}
		layers = append(layers, layer)
	}
	img, err := mutate.AppendLayers(empty.Image, layers...)
	if err != nil {
		t.Fatal(err)
	}
	cf, err := img.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	cf.Architecture = runtime.GOARCH
	cf.OS = "linux"
	cf.Config = v1.Config{Entrypoint: []string{"/bin/toolbox"}, Cmd: []string{"echo", "archive"}, Env: []string{"ARCHIVE=fixture"}, WorkingDir: "/work", User: "1000:1001", StopSignal: "SIGUSR1", Shell: []string{"/bin/toolbox", "sh", "-c"}, Healthcheck: &v1.HealthConfig{Test: []string{"CMD", "/bin/toolbox", "true"}, Interval: time.Second, Timeout: time.Second, StartPeriod: time.Second, Retries: 2}}
	img, err = mutate.ConfigFile(img, cf)
	if err != nil {
		t.Fatal(err)
	}
	img = mutate.MediaType(img, types.OCIManifestSchema1)
	img = mutate.ConfigMediaType(img, types.OCIConfigJSON)
	dir := t.TempDir()
	lp, err := layout.Write(dir, empty.Index)
	if err != nil {
		t.Fatal(err)
	}
	if err := lp.AppendImage(img); err != nil {
		t.Fatal(err)
	}

	// Add the newer health extension through raw OCI config bytes, independent
	// of the dependency's older typed HealthConfig.
	idx, err := lp.ImageIndex()
	if err != nil {
		t.Fatal(err)
	}
	im, err := idx.IndexManifest()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "blobs/sha256", im.Manifests[0].Digest.Hex))
	if err != nil {
		t.Fatal(err)
	}
	var manifest v1.Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(filepath.Join(dir, "blobs/sha256", manifest.Config.Digest.Hex))
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	config["config"].(map[string]any)["Healthcheck"].(map[string]any)["StartInterval"] = float64(time.Second)
	raw, err = json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(raw)
	manifest.Config.Digest = v1.Hash{Algorithm: "sha256", Hex: hex.EncodeToString(hash[:])}
	manifest.Config.Size = int64(len(raw))
	if err := os.WriteFile(filepath.Join(dir, "blobs/sha256", manifest.Config.Digest.Hex), raw, 0600); err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	hash = sha256.Sum256(raw)
	im.Manifests[0].Digest = v1.Hash{Algorithm: "sha256", Hex: hex.EncodeToString(hash[:])}
	im.Manifests[0].Size = int64(len(raw))
	if err := os.WriteFile(filepath.Join(dir, "blobs/sha256", im.Manifests[0].Digest.Hex), raw, 0600); err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(im)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	var outer bytes.Buffer
	writer := tar.NewWriter(&outer)
	if err := filepath.WalkDir(dir, func(filename string, entry fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		name, _ := filepath.Rel(dir, filename)
		info, e := entry.Info()
		if e != nil {
			return e
		}
		h, e := tar.FileInfoHeader(info, "")
		if e != nil {
			return e
		}
		h.Name = filepath.ToSlash(name)
		if e := writer.WriteHeader(h); e != nil {
			return e
		}
		if entry.IsDir() {
			return nil
		}
		f, e := os.Open(filename)
		if e != nil {
			return e
		}
		_, e = io.Copy(writer, f)
		return errors.Join(e, f.Close())
	}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return outer.Bytes()
}
func TestOCIArchiveThirdPartyLayoutAndNativeMetadata(t *testing.T) {
	a, err := readOCIArchive(context.Background(), t.TempDir(), bytes.NewReader(archiveFixture(t)))
	if err != nil {
		t.Fatal(err)
	}
	img, err := a.nativeImage(context.Background(), runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	if len(img.manifest.Layers) != 2 || img.config.Config.User != "1000:1001" || img.config.Config.WorkingDir != "/work" || img.config.Config.StopSignal != "SIGUSR1" {
		t.Fatalf("metadata: %+v", img.config)
	}
	if _, err := a.nativeImage(context.Background(), "unsupported"); err == nil {
		t.Fatal("accepted foreign platform")
	}
}
func rewriteOuter(t *testing.T, data []byte, change func(*tar.Header, []byte) (*tar.Header, []byte)) []byte {
	t.Helper()
	r := tar.NewReader(bytes.NewReader(data))
	var out bytes.Buffer
	w := tar.NewWriter(&out)
	for {
		h, e := r.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		raw, e := io.ReadAll(r)
		if e != nil {
			t.Fatal(e)
		}
		h, raw = change(h, raw)
		if h == nil {
			continue
		}
		h.Size = int64(len(raw))
		if e := w.WriteHeader(h); e != nil {
			t.Fatal(e)
		}
		if _, e := w.Write(raw); e != nil {
			t.Fatal(e)
		}
	}
	if e := w.Close(); e != nil {
		t.Fatal(e)
	}
	return out.Bytes()
}
func TestOCIArchiveRejectsUnsafeCorruptAndIncompleteStreams(t *testing.T) {
	fixture := archiveFixture(t)
	cases := map[string][]byte{"no terminator": fixture[:len(fixture)-1024], "one terminator": fixture[:len(fixture)-512], "trailing": append(append([]byte(nil), fixture...), 1), "truncated": fixture[:len(fixture)/2]}
	for _, kind := range []string{"traversal", "symlink", "duplicate", "digest", "version", "oversized metadata", "fifo", "duplicate JSON"} {
		kind := kind
		cases[kind] = rewriteOuter(t, fixture, func(h *tar.Header, raw []byte) (*tar.Header, []byte) {
			if h.Name == "oci-layout" {
				switch kind {
				case "traversal":
					h.Name = "../escape"
				case "symlink":
					h.Typeflag = tar.TypeSymlink
					h.Linkname = "/etc/passwd"
					raw = nil
				case "duplicate":
					h.Name = "."
				case "duplicate JSON":
					raw = []byte(`{"imageLayoutVersion":"1.0.0","imageLayoutVersion":"1.0.0"}`)
				case "version":
					raw = []byte(`{"imageLayoutVersion":"9"}`)
				case "oversized metadata":
					raw = make([]byte, maxOCIMetadataBytes+1)
				case "fifo":
					h.Typeflag = tar.TypeFifo
					raw = nil
				}
			}
			if kind == "digest" && len(h.Name) > len("blobs/sha256/") && h.Typeflag == tar.TypeReg {
				raw = append(raw, 1)
			}
			return h, raw
		})
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := readOCIArchive(context.Background(), t.TempDir(), bytes.NewReader(data)); err == nil {
				t.Fatal("accepted invalid OCI archive")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := readOCIArchive(ctx, t.TempDir(), bytes.NewReader(fixture)); err == nil {
		t.Fatal("accepted canceled load")
	}
}
func TestOCIDescriptorSizeAndConfigValidation(t *testing.T) {
	a, err := readOCIArchive(context.Background(), t.TempDir(), bytes.NewReader(archiveFixture(t)))
	if err != nil {
		t.Fatal(err)
	}
	var idx v1.IndexManifest
	if err := a.decodeFile("index.json", &idx); err != nil {
		t.Fatal(err)
	}
	name, _ := a.blobName(idx.Manifests[0])
	var manifest v1.Manifest
	if err := a.decodeFile(name, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.Config.Size++
	raw, _ := json.Marshal(manifest)
	hash := sha256.Sum256(raw)
	replacement := "blobs/sha256/" + hex.EncodeToString(hash[:])
	if err := os.WriteFile(filepath.Join(a.dir, replacement), raw, 0600); err != nil {
		t.Fatal(err)
	}
	a.sizes[replacement] = int64(len(raw))
	idx.Manifests[0].Size = int64(len(raw))
	idx.Manifests[0].Digest = v1.Hash{Algorithm: "sha256", Hex: hex.EncodeToString(hash[:])}
	raw, _ = json.Marshal(idx)
	if err := os.WriteFile(filepath.Join(a.dir, "index.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	a.sizes["index.json"] = int64(len(raw))
	if _, err := a.nativeImage(context.Background(), runtime.GOARCH); err == nil {
		t.Fatal("accepted descriptor size mismatch")
	}
}
