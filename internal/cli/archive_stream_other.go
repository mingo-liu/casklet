//go:build !linux && !darwin

package cli

import (
	"context"
	"os"
)

// Image and volume stores reject unsupported platforms before stream setup.
func cancelableStreamFile(_ context.Context, file *os.File) (*os.File, func(), error) {
	return file, func() {}, nil
}
