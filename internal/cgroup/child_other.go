//go:build !linux

package cgroup

import "errors"

func (g *Group) NewChild() (*Group, error) {
	return nil, errors.New("exec cgroups require Linux")
}
