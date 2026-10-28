//go:build !linux

package container

import "context"

func Events(context.Context, EventQuery, func(Event) error) error { return errUnsupportedStore }
