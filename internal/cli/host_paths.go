package cli

import (
	"path/filepath"

	"github.com/mingo-liu/casklet/internal/config"
	"github.com/mingo-liu/casklet/internal/runargs"
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
	if action != "run" && action != "doctor" && action != "project-preflight" {
		return result, nil
	}
	err := runargs.Walk(result, func(name, value string, index, end int) error {
		switch name {
		case "rootfs":
			if value == "builtin:busybox" {
				return nil
			}
			var err error
			value, err = filepath.Abs(value)
			if err != nil {
				return err
			}
		case "mount":
			mount, err := config.ParseMount(value)
			if err != nil {
				return err
			}
			if mount.Type == "volume" {
				return nil
			}
			mount.Source, err = filepath.EvalSymlinks(mount.Source)
			if err != nil {
				return err
			}
			value = "type=bind,source=" + mount.Source + ",target=" + mount.Target
			if mount.ReadOnly {
				value += ",readonly"
			}
		default:
			return nil
		}
		runargs.SetValue(result, index, end, value)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
