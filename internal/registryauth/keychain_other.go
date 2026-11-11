//go:build !darwin

package registryauth

import "errors"

// OpenStore reports that native registry credential storage requires macOS.
func OpenStore() (Store, error) {
	return nil, errors.New("registry login and logout are available only on macOS")
}
