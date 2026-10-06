//go:build !linux && !darwin

package rootfs

import "os"

func openSource(path string) (*os.File, error) { return os.Open(path) }

func openRootSource(root *os.Root, name string) (*os.File, error) { return root.Open(name) }
