//go:build !linux

package rootfs

import (
	"errors"

	"github.com/mingo-liu/mini-docker/internal/config"
)

func ValidateMountSources(mounts []config.BindMount, root string) error {
	return errors.New("bind mounts require Linux")
}
