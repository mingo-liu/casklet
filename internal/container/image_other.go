//go:build !linux

package container

import "context"

func ImageReferenced(context.Context, string) (bool, error) { return false, errUnsupportedStore }
