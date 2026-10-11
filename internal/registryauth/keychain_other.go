//go:build !darwin

package registryauth

import "errors"

func OpenStore() (Store, error) {
	return nil, errors.New("registry login and logout are available only on macOS")
}
