//go:build !linux

package image

import "context"

func (*Store) pruneBlobs(context.Context, bool) error { return errUnsupported }
