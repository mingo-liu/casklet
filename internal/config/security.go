package config

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// IDMapping maps a contiguous range of container IDs to host IDs.
type IDMapping struct {
	ContainerID uint32 `json:"container_id"`
	HostID      uint32 `json:"host_id"`
	Size        uint32 `json:"size"`
}

func ParseIDMapping(value string) (IDMapping, error) {
	var mapping IDMapping
	parts := strings.Split(value, ":")
	if len(parts) != 3 {
		return mapping, errors.New("ID mapping must be CONTAINER_ID:HOST_ID:SIZE")
	}
	values := make([]uint32, 3)
	for i, part := range parts {
		if part == "" || strings.Trim(part, "0123456789") != "" {
			return mapping, errors.New("ID mapping requires unsigned decimal integers")
		}
		value, err := strconv.ParseUint(part, 10, 32)
		if err != nil {
			return mapping, fmt.Errorf("invalid ID mapping: %w", err)
		}
		values[i] = uint32(value)
	}
	return IDMapping{values[0], values[1], values[2]}, nil
}

func MappedID(id uint32, mappings []IDMapping) (uint32, bool) {
	for _, m := range mappings {
		if id >= m.ContainerID && uint64(id) < uint64(m.ContainerID)+uint64(m.Size) {
			return m.HostID + id - m.ContainerID, true
		}
	}
	return 0, false
}

func validateMappings(mappings []IDMapping) error {
	if len(mappings) == 0 || len(mappings) > 340 {
		return errors.New("provide between 1 and 340 ID mappings")
	}
	for i, m := range mappings {
		if m.Size == 0 || uint64(m.ContainerID)+uint64(m.Size) > math.MaxUint32 || uint64(m.HostID)+uint64(m.Size) > math.MaxUint32 {
			return errors.New("ID mapping is empty or includes an invalid ID")
		}
		for _, previous := range mappings[:i] {
			overlap := func(a, b uint32, as, bs uint32) bool {
				return uint64(a) < uint64(b)+uint64(bs) && uint64(b) < uint64(a)+uint64(as)
			}
			if overlap(m.ContainerID, previous.ContainerID, m.Size, previous.Size) || overlap(m.HostID, previous.HostID, m.Size, previous.Size) {
				return errors.New("ID mapping ranges must not overlap")
			}
		}
	}
	if _, ok := MappedID(0, mappings); !ok {
		return errors.New("ID mappings must include container ID 0")
	}
	return nil
}

func (c Config) SeccompProfile() string {
	if c.Seccomp == "" {
		return "default"
	}
	return c.Seccomp
}

func (c Config) ValidateSecurity() error {
	if c.SeccompProfile() != "default" && c.SeccompProfile() != "unconfined" {
		return errors.New("seccomp must be default or unconfined")
	}
	if c.Rootless {
		if !c.UserNS || c.Image != "" || c.NetworkMode() != "none" {
			return errors.New("rootless requires a user namespace, a directory rootfs, and --network none")
		}
		if len(c.UIDMappings) != 1 || len(c.GIDMappings) != 1 || c.UIDMappings[0].ContainerID != 0 || c.GIDMappings[0].ContainerID != 0 || c.UIDMappings[0].Size != 1 || c.GIDMappings[0].Size != 1 {
			return errors.New("rootless supports only a single caller UID/GID mapped to container 0:0")
		}
		state := fmt.Sprintf("/run/user/%d/casklet", c.UIDMappings[0].HostID)
		for _, mount := range c.Mounts {
			if pathsOverlap(mount.Source, state) {
				return errors.New("mount source overlaps rootless runtime storage")
			}
		}
	}
	if c.UserNS {
		if c.NetworkMode() != "none" {
			return errors.New("user namespaces currently require --network none")
		}
	}
	if !c.UserNS {
		if len(c.UIDMappings) != 0 || len(c.GIDMappings) != 0 {
			return errors.New("ID mappings require --userns")
		}
		return nil
	}
	if err := validateMappings(c.UIDMappings); err != nil {
		return fmt.Errorf("UID mappings: %w", err)
	}
	if err := validateMappings(c.GIDMappings); err != nil {
		return fmt.Errorf("GID mappings: %w", err)
	}
	user := User{}
	if c.User != nil {
		user = *c.User
	}
	if _, ok := MappedID(user.UID, c.UIDMappings); !ok {
		return errors.New("container UID is not mapped")
	}
	if _, ok := MappedID(user.GID, c.GIDMappings); !ok {
		return errors.New("container GID is not mapped")
	}
	return nil
}
