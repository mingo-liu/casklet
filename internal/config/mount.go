package config

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
)

// BindMount exposes an existing host directory at a container path.
type BindMount struct {
	Type     string `json:"type,omitempty"`
	Source   string `json:"source"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"read_only"`
}

// ParseMount parses the repeatable --mount option without filesystem access.
func ParseMount(value string) (BindMount, error) {
	var mount BindMount
	seen := make(map[string]bool)
	for _, field := range strings.Split(value, ",") {
		key, val, assigned := strings.Cut(field, "=")
		if seen[key] {
			return mount, fmt.Errorf("duplicate mount option %q", key)
		}
		seen[key] = true
		switch key {
		case "type":
			if !assigned || (val != "bind" && val != "volume") {
				return mount, errors.New("mount type must be bind or volume")
			}
			if val == "volume" {
				mount.Type = val
			}
		case "source":
			if !assigned {
				return mount, errors.New("mount source requires a value")
			}
			mount.Source = val
		case "target":
			if !assigned {
				return mount, errors.New("mount target requires a value")
			}
			mount.Target = val
		case "readonly":
			if assigned {
				return mount, errors.New("mount readonly does not accept a value")
			}
			mount.ReadOnly = true
		default:
			return mount, fmt.Errorf("unknown mount option %q", key)
		}
	}
	if !seen["type"] {
		return mount, errors.New("mount requires type=bind or type=volume")
	}
	return mount, ValidateMounts([]BindMount{mount}, "")
}

func pathsOverlap(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

// ValidateMounts checks the mount protocol, including duplicate and nested targets.
// Existing directories and symlink safety are checked separately on Linux.
func ValidateMounts(mounts []BindMount, rootfs string) error {
	if len(mounts) > 32 {
		return errors.New("at most 32 bind mounts are supported")
	}
	for i, mount := range mounts {
		if mount.Type != "" && mount.Type != "bind" && mount.Type != "volume" {
			return errors.New("invalid mount type")
		}
		if mount.Type == "volume" {
			if err := ValidateVolumeName(mount.Source); err != nil {
				return err
			}
		}
		for _, entry := range []struct{ name, value string }{{"source", mount.Source}, {"target", mount.Target}} {
			if entry.name == "source" && mount.Type == "volume" {
				continue
			}
			if !path.IsAbs(entry.value) || entry.value == "/" || path.Clean(entry.value) != entry.value || strings.ContainsAny(entry.value, "\x00\n\r") {
				return fmt.Errorf("mount %s must be a clean absolute path other than / without NUL or newlines", entry.name)
			}
		}
		for _, reserved := range []string{"/proc", "/sys", "/dev", "/var/lib/casklet", "/tmp/casklet-userns"} {
			if mount.Type != "volume" && pathsOverlap(mount.Source, reserved) {
				return fmt.Errorf("mount source overlaps protected host path %s", reserved)
			}
		}
		if mount.Type != "volume" && rootfs != "" && path.IsAbs(rootfs) && pathsOverlap(mount.Source, path.Clean(rootfs)) {
			return errors.New("mount source and rootfs template must not overlap")
		}
		for _, reserved := range []string{"/proc", "/dev", "/sys"} {
			if pathsOverlap(mount.Target, reserved) {
				return fmt.Errorf("mount target overlaps reserved container path %s", reserved)
			}
		}
		if mount.Target == "/tmp" {
			return errors.New("mount target cannot replace /tmp; use a subdirectory")
		}
		for _, previous := range mounts[:i] {
			if pathsOverlap(mount.Target, previous.Target) {
				return errors.New("mount targets must not overlap")
			}
		}
	}
	return nil
}

// VolumeRoot is guest-local storage; volume names never refer to Mac paths.
const VolumeRoot = "/var/lib/casklet/volumes"

var volumeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,62}$`)

func ValidateVolumeName(name string) error {
	if !volumeName.MatchString(name) {
		return errors.New("volume name must contain 1-63 letters, digits, underscores, periods, or hyphens and start alphanumeric")
	}
	return nil
}
func (m BindMount) SourcePath() string {
	if m.Type == "volume" {
		return path.Join(VolumeRoot, m.Source, "data")
	}
	return m.Source
}
func VolumeNames(mounts []BindMount) []string {
	seen := map[string]bool{}
	for _, m := range mounts {
		if m.Type == "volume" {
			seen[m.Source] = true
		}
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
