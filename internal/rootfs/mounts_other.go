//go:build !linux

package rootfs

func checkMounts(source string) error { return nil }

func CheckUnmounted(source string) error { return nil }
