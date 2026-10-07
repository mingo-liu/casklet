//go:build !darwin

package machine

import (
	"errors"
	"os"
)

func checkTerminal(*os.File) error { return errors.New("the public client requires macOS") }
