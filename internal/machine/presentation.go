package machine

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
)

const maxInspectionBytes = 4 << 20

// inspectionOutput bounds a nonstreaming response while continuing to drain SSH.
// A guest failure must retain its exit status even if its output is oversized.
type inspectionOutput struct {
	data     bytes.Buffer
	overflow bool
}

func (output *inspectionOutput) Write(data []byte) (int, error) {
	length := len(data)
	remaining := maxInspectionBytes - output.data.Len()
	if length > remaining {
		output.overflow = true
		data = data[:remaining]
	}
	_, _ = output.data.Write(data)
	return length, nil
}

func guestResult(instance Instance, inspection *inspectionOutput, stdout io.Writer, err error) (int, error) {
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() >= 0 {
			return exit.ExitCode(), nil
		}
		return 125, err
	}
	if inspection != nil {
		if inspection.overflow {
			return 125, errors.New("guest inspection exceeds the 4 MiB response limit")
		}
		if err := presentInspection(instance, inspection.data.Bytes(), stdout); err != nil {
			return 125, err
		}
	}
	return 0, nil
}

// macPath reverses shared-path translation without accessing source directories:
// retained containers remain inspectable after their original sources disappear.
func (instance Instance) macPath(guestPath string) string {
	if guestPath == guestRootFS {
		return BuiltinRootFS
	}
	if !filepath.IsAbs(guestPath) {
		return guestPath
	}
	result, longest := guestPath, -1
	for _, mount := range instance.Config.Mounts {
		point := mount.MountPoint
		if point == "" {
			point = mount.Location
		}
		if !filepath.IsAbs(point) || !filepath.IsAbs(mount.Location) {
			continue
		}
		point = filepath.Clean(point)
		if len(point) <= longest || !within(point, guestPath) {
			continue
		}
		relative, _ := filepath.Rel(point, guestPath)
		result, longest = filepath.Join(mount.Location, relative), len(point)
	}
	return result
}

func presentInspection(instance Instance, data []byte, stdout io.Writer) error {
	var inspection map[string]json.RawMessage
	if err := json.Unmarshal(data, &inspection); err != nil || inspection == nil {
		return errors.New("invalid guest inspection: expected a JSON object")
	}
	var cfg map[string]json.RawMessage
	if err := json.Unmarshal(inspection["config"], &cfg); err != nil || cfg == nil {
		return errors.New("invalid guest inspection: expected a configuration object")
	}
	guestResources := map[string]json.RawMessage{}
	for _, field := range []string{"rootfs", "mounts", "publish"} {
		value, exists := cfg[field]
		if !exists {
			return fmt.Errorf("invalid guest inspection: configuration is missing %s", field)
		}
		guestResources[field] = value
	}
	rootfs, err := inspectionString(cfg["rootfs"])
	if err != nil {
		return fmt.Errorf("invalid guest inspection rootfs: %w", err)
	}
	cfg["rootfs"], _ = json.Marshal(instance.macPath(rootfs))
	for _, field := range []string{"mounts", "publish"} {
		var entries []map[string]json.RawMessage
		if err := json.Unmarshal(cfg[field], &entries); err != nil {
			return fmt.Errorf("invalid guest inspection %s: expected an array", field)
		}
		for _, entry := range entries {
			key := "source"
			if field == "publish" {
				key = "host_ip"
			}
			value, err := inspectionString(entry[key])
			if err != nil {
				return fmt.Errorf("invalid guest inspection %s %s: %w", field, key, err)
			}
			if field == "mounts" {
				value = instance.macPath(value)
			} else {
				switch value {
				case "127.0.0.2":
					value = "0.0.0.0"
				case "127.0.0.3":
					value = "127.0.0.1"
				}
			}
			entry[key], _ = json.Marshal(value)
		}
		cfg[field], _ = json.Marshal(entries)
	}
	inspection["config"], _ = json.Marshal(cfg)
	inspection["guest_resources"], _ = json.Marshal(guestResources)
	// Marshal the complete response before writing to avoid partial malformed JSON.
	result, err := json.MarshalIndent(inspection, "", "  ")
	if err != nil {
		return fmt.Errorf("encode host inspection: %w", err)
	}
	result = append(result, '\n')
	written, err := stdout.Write(result)
	if err == nil && written != len(result) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return fmt.Errorf("write host inspection: %w", err)
	}
	return nil
}

func inspectionString(data json.RawMessage) (string, error) {
	var value string
	if len(data) == 0 || bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return "", errors.New("expected a string")
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return "", errors.New("expected a string")
	}
	return value, nil
}
