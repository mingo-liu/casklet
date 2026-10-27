//go:build linux || darwin

package copyfs

import (
	"crypto/rand"
	"encoding/hex"
	"os"

	"golang.org/x/sys/unix"
)

func openRegular(root *os.Root, name string) (*os.File, error) {
	return root.OpenFile(name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
}

func createTemporary(root *os.Root) (*os.File, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return nil, err
	}
	name := ".casklet-cp-" + hex.EncodeToString(token[:])
	file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	return file, nil
}
