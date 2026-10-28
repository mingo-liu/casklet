//go:build !linux

package rootfs

import (
	"errors"
	"github.com/mingo-liu/casklet/internal/config"
)

func ValidateSecretSources(secrets []config.Secret) error {
	if len(secrets) == 0 {
		return nil
	}
	return errors.New("secret injection requires Linux")
}
