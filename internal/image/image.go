// Package image stores immutable, content-addressed root filesystem snapshots.
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

	"github.com/mingo-liu/mini-docker/internal/config"
)

var ErrNotFound = errors.New("image not found")
var ErrInUse = errors.New("image is referenced; remove its containers before deleting it")

// Record is the public metadata for an imported image.
type Record struct {
	ID           string    `json:"id"`
	Architecture string    `json:"architecture"`
	CreatedAt    time.Time `json:"created_at"`
	SizeBytes    int64     `json:"size_bytes"`
}

// ReferenceCheck runs under the image store lock. It must not acquire image locks.
type ReferenceCheck func(context.Context, string) (bool, error)

func ValidateID(id string) error { return config.ValidateImageID(id) }

// Identity hashes sorted paths, copied modes, link targets and regular contents.
// Ownership and timestamps are excluded because the runtime does not copy them.
func Identity(ctx context.Context, path, architecture string) (string, int64, error) {
	root, err := os.OpenRoot(path)
	if err != nil {
		return "", 0, err
	}
	defer root.Close()
	hash := sha256.New()
	encoder := json.NewEncoder(hash)
	if err := encoder.Encode(struct {
		Version      int
		Architecture string
	}{1, architecture}); err != nil {
		return "", 0, err
	}
	var size int64
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
		}{Name: filepath.ToSlash(name), Mode: uint32(info.Mode())}
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
			count, copyErr := io.Copy(digest, contextReader{ctx: ctx, r: file})
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
	const root = "/var/lib/mini-docker/images"
	return path == root || strings.HasPrefix(path, root+"/")
}
