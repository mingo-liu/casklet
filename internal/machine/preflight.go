package machine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mingo-liu/mini-docker/internal/config"
	"github.com/mingo-liu/mini-docker/internal/rootfs"
)

// visitHostResources skips option values and never reads workload arguments.
// Invocations have already passed the CLI parser before reaching this layer.
func visitHostResources(args []string, visit func(string, string) error) error {
	if len(args) >= 3 && args[0] == "image" && args[1] == "import" {
		return visit("rootfs", args[len(args)-1])
	}
	if len(args) == 0 || (args[0] != "run" && args[0] != "doctor") {
		return nil
	}
	booleans := map[string]bool{"d": true, "detach": true, "i": true, "interactive": true, "t": true, "tty": true, "it": true, "ti": true, "read-only": true, "rootless": true, "userns": true}
	for i := 1; i < len(args); i++ {
		if args[i] == "--" {
			break
		}
		name, value, inline := strings.Cut(strings.TrimLeft(args[i], "-"), "=")
		if booleans[name] {
			continue
		}
		if !inline {
			i++
			if i >= len(args) {
				return fmt.Errorf("missing value for --%s", name)
			}
			value = args[i]
		}
		if err := visit(name, value); err != nil {
			return err
		}
	}
	return nil
}

func validateMacPort(mapping config.PortMapping) error {
	if mapping.HostPort == 22 {
		return errors.New("host port 22 is reserved by Lima; publish another port")
	}
	if mapping.HostIP != "0.0.0.0" && mapping.HostIP != "127.0.0.1" {
		return errors.New("macOS published ports support host addresses 0.0.0.0 and 127.0.0.1")
	}
	return nil
}

func localPreflight(args []string) error {
	var canonicalRoot string
	var mounts []config.BindMount
	if err := visitHostResources(args, func(name, value string) error {
		switch name {
		case "rootfs":
			canonicalRoot = ""
			if value == BuiltinRootFS {
				return nil
			}
			root, err := rootfs.Validate(value)
			if err != nil {
				return fmt.Errorf("rootfs template %s: %w", value, err)
			}
			canonicalRoot = root
		case "mount":
			mount, err := config.ParseMount(value)
			if err != nil {
				return err
			}
			mount.Source, err = sharedDirectory(mount.Source)
			if err != nil {
				return fmt.Errorf("bind source: %w", err)
			}
			mounts = append(mounts, mount)
		case "p", "publish":
			mapping, err := config.ParsePortMapping(value)
			if err != nil {
				return err
			}
			return validateMacPort(mapping)
		}
		return nil
	}); err != nil {
		return err
	}
	// Check actual source relationships after resolving symlinks, independently
	// of option order and before creating or starting the VM.
	if err := config.ValidateMounts(mounts, canonicalRoot); err != nil {
		return err
	}
	return checkPorts(args)
}

func (m *Machine) preflightShares(ctx context.Context, args []string) error {
	needsShares := false
	if err := visitHostResources(args, func(name, value string) error {
		needsShares = needsShares || name == "mount" || (name == "rootfs" && value != BuiltinRootFS)
		return nil
	}); err != nil {
		return err
	}
	if !needsShares {
		return nil
	}
	instance, err := m.instance(ctx)
	if err != nil {
		return err
	}
	if instance == nil {
		instance = &Instance{}
		data, err := configData(defaultOptions())
		if err != nil {
			return err
		}
		if err := json.Unmarshal(data, &instance.Config); err != nil {
			return err
		}
	}
	_, err = guestArguments(*instance, args)
	return err
}

// exportParent checks the destination without creating host directories. The
// nearest existing parent determines whether new descendants are shared.
func exportParent(destination string) (string, error) {
	absolute, err := filepath.Abs(destination)
	if err != nil {
		return "", err
	}
	if _, err := os.Lstat(absolute); !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("rootfs destination already exists or is unavailable: %s; choose a new destination directory", absolute)
	}
	parent := filepath.Dir(absolute)
	for {
		if _, err := os.Stat(parent); err == nil {
			return sharedDirectory(parent)
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		next := filepath.Dir(parent)
		if next == parent {
			return "", fmt.Errorf("rootfs parent is unavailable: %s", parent)
		}
		parent = next
	}
}
