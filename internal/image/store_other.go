//go:build !linux

package image

import (
	"context"
	"errors"
	"io"
	"os"
)

// Store represents image operations that require the internal Linux engine.
type Store struct{}

var errUnsupported = errors.New("local image management requires Linux")

// OpenStore reports that local image storage requires Linux.
func OpenStore() (*Store, error) { return nil, errUnsupported }

// Tag reports that this image operation requires Linux.
func (*Store) Tag(context.Context, string, string) error { return errUnsupported }

// Push reports that this image operation requires Linux.
func (*Store) Push(context.Context, string, string) (string, error) { return "", errUnsupported }

// Import reports that this image operation requires Linux.
func (*Store) Import(context.Context, string) (Record, error) { return Record{}, errUnsupported }

// List reports that this image operation requires Linux.
func (*Store) List(context.Context) ([]Record, error) { return nil, errUnsupported }

// Resolve reports that this image operation requires Linux.
func (*Store) Resolve(context.Context, string) (Record, error) { return Record{}, errUnsupported }

// Pull reports that this image operation requires Linux.
func (*Store) Pull(context.Context, string) (Record, error) { return Record{}, errUnsupported }

// Acquire reports that this image operation requires Linux.
func (*Store) Acquire(context.Context, string) (Record, string, *os.File, error) {
	return Record{}, "", nil, errUnsupported
}

// Remove reports that this image operation requires Linux.
func (*Store) Remove(context.Context, string, ReferenceCheck) error { return errUnsupported }

// Load reports that this image operation requires Linux.
func (*Store) Load(context.Context, io.Reader) (Record, error) { return Record{}, errUnsupported }

// Save reports that this image operation requires Linux.
func (*Store) Save(context.Context, string, io.Writer) error { return errUnsupported }

func (*Store) remove(context.Context, string, ReferenceCheck, bool) error { return errUnsupported }
