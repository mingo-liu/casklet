//go:build !linux && !darwin

package remote

import "os"

// The product's standard-descriptor transport supports Darwin and Linux.
func portCheckFile(*os.File) (*os.File, func(), error) {
	return nil, func() {}, nil
}
