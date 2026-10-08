package cli

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mingo-liu/casklet/internal/config"
)

// hostPathArguments changes only CLI path values, never workload arguments or
// values belonging to other options. The guest parses identities and defaults.
func hostPathArguments(args []string, action string) ([]string, error) {
	result := append([]string(nil), args...)
	if action == "image-import" {
		absolute, err := filepath.Abs(result[len(result)-1])
		if err != nil {
			return nil, err
		}
		result[len(result)-1] = absolute
		return result, nil
	}
	if action != "run" && action != "doctor" {
		return result, nil
	}
	booleans := map[string]bool{"d": true, "detach": true, "i": true, "interactive": true, "t": true, "tty": true, "it": true, "ti": true, "read-only": true, "rootless": true, "userns": true}
	for i := 1; i < len(result); i++ {
		arg := result[i]
		if arg == "--" {
			break
		}
		name, value, inline := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		if booleans[name] {
			continue
		}
		index := i
		if !inline {
			i++
			index = i
			if i >= len(result) {
				return nil, fmt.Errorf("missing value for %s", arg)
			}
			value = result[i]
		}
		switch name {
		case "rootfs":
			if value == "builtin:busybox" {
				continue
			}
			var err error
			value, err = filepath.Abs(value)
			if err != nil {
				return nil, err
			}
		case "mount":
			mount, err := config.ParseMount(value)
			if err != nil {
				return nil, err
			}
			if mount.Type == "volume" {
				continue
			}
			mount.Source, err = filepath.EvalSymlinks(mount.Source)
			if err != nil {
				return nil, err
			}
			value = "type=bind,source=" + mount.Source + ",target=" + mount.Target
			if mount.ReadOnly {
				value += ",readonly"
			}
		default:
			continue
		}
		if inline {
			result[index] = strings.SplitN(arg, "=", 2)[0] + "=" + value
		} else {
			result[index] = value
		}
	}
	return result, nil
}
