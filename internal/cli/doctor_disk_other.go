//go:build !linux

package cli

import (
	"errors"
	"github.com/mingo-liu/casklet/internal/storage"
)

func doctorFilesystem() (storage.Filesystem, error) {
	return storage.Filesystem{}, errors.New("guest filesystem diagnostics require Linux")
}
