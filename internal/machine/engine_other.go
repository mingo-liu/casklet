//go:build !darwin

package machine

import "os"

func bundledEngine() ([]byte, error) { return nil, os.ErrNotExist }
