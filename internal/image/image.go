// Package image pulls OCI/Docker images and stores immutable filesystem snapshots.
package image

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mingo-liu/casklet/internal/config"
	"github.com/mingo-liu/casklet/internal/rootfs"
)

var ErrNotFound = errors.New("image not found")
var ErrAmbiguousID = errors.New("image ID prefix is ambiguous")
var ErrInUse = errors.New("image is referenced; remove its containers before deleting it")

// Record is the public metadata for an imported or pulled image.
type Record struct {
	ID             string        `json:"id"`
	Architecture   string        `json:"architecture"`
	CreatedAt      time.Time     `json:"created_at"`
	SizeBytes      int64         `json:"size_bytes"`
	Config         *LaunchConfig `json:"config,omitempty"`
	ManifestDigest string        `json:"manifest_digest,omitempty"`
	References     []string      `json:"references,omitempty"`
}

// ReferenceCheck runs under the image store lock. It must not acquire image locks.
type ReferenceCheck func(context.Context, string) (bool, error)

func ValidateID(id string) error { return config.ValidateImageID(id) }

// IsIDReference recognizes local hexadecimal IDs and prefixes, with an optional
// sha256: prefix. Persisted identities still require ValidateID.
func IsIDReference(value string) bool {
	value = strings.TrimPrefix(value, "sha256:")
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for _, ch := range value {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return false
		}
	}
	return true
}

// ValidateIDReference validates user input without weakening persisted IDs.
func ValidateIDReference(value string) error {
	if !IsIDReference(value) {
		return errors.New("image ID must contain 1 to 64 lowercase hexadecimal digits, optionally prefixed with sha256:")
	}
	return nil
}

// Identity hashes sorted paths, copied modes, link targets and regular contents.
// Directory imports retain their original identity format without ownership.
// OCI identities additionally cover launch defaults and numeric ownership.
// Timestamps are excluded because the runtime does not copy them.
func Identity(ctx context.Context, path, architecture string, launch ...*LaunchConfig) (string, int64, error) {
	root, err := os.OpenRoot(path)
	if err != nil {
		return "", 0, err
	}
	defer root.Close()
	hash := sha256.New()
	encoder := json.NewEncoder(hash)
	owned := len(launch) > 0 && launch[0] != nil
	if owned {
		if err := encoder.Encode(launch[0]); err != nil {
			return "", 0, err
		}
	}
	if err := encoder.Encode(struct {
		Version      int
		Architecture string
	}{1, architecture}); err != nil {
		return "", 0, err
	}
	var size int64
	buffer := make([]byte, 32*1024)
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := root.Lstat(name)
		if err != nil {
			return err
		}
		item := struct {
			Name    string
			Mode    uint32
			Size    int64
			Link    string
			Content string
			UID     *uint32 `json:",omitempty"`
			GID     *uint32 `json:",omitempty"`
		}{Name: filepath.ToSlash(name), Mode: uint32(info.Mode())}
		if owned {
			uid, gid := rootfs.Ownership(info)
			item.UID, item.GID = &uid, &gid
		}
		switch {
		case info.IsDir():
		case info.Mode()&os.ModeSymlink != 0:
			item.Link, err = root.Readlink(name)
			if err != nil {
				return err
			}
		case info.Mode().IsRegular():
			file, err := root.Open(name)
			if err != nil {
				return err
			}
			digest := sha256.New()
			count, copyErr := io.CopyBuffer(digest, contextReader{ctx: ctx, r: file}, buffer)
			closeErr := file.Close()
			if err := errors.Join(copyErr, closeErr); err != nil {
				return err
			}
			item.Size, item.Content = count, hex.EncodeToString(digest.Sum(nil))
			size += count
		default:
			return errors.New("image contains an unsupported special file")
		}
		return encoder.Encode(item)
	})
	if err != nil {
		return "", 0, err
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), size, nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

// IsStorePath rejects bypassing image leases through a raw rootfs path.
func IsStorePath(path string) bool {
	const root = "/var/lib/casklet/images"
	return path == root || strings.HasPrefix(path, root+"/")
}
