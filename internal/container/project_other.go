//go:build !linux

package container

import "context"

func RemoveProjectService(context.Context, string, uint64, map[string]string) error {
	return errUnsupportedStore
}

func PreflightProjectStart(context.Context, string, uint64, map[string]string) error {
	return errUnsupportedStore
}
