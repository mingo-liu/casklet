// Package machine manages the private Linux engine used by the macOS client.
package machine

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const Name = "mini-docker-runtime"
const BuiltinRootFS = "builtin:busybox"
const guestRootFS = "/var/lib/mini-docker/templates/busybox"
const guestEngine = "/usr/local/bin/mdocker"

type Options struct {
	CPUs   int
	Memory int
	Disk   int
	Mounts []string
}

type Instance struct {
	Name          string `json:"name"`
	Status        string `json:"status"`
	Arch          string `json:"arch"`
	SSHConfigFile string `json:"sshConfigFile"`
	Config        struct {
		Plain  bool `json:"plain"`
		Mounts []struct {
			Location   string `json:"location"`
			MountPoint string `json:"mountPoint"`
			Writable   bool   `json:"writable"`
		} `json:"mounts"`
	} `json:"config"`
}

func defaultOptions() Options { return Options{CPUs: 4, Memory: 4, Disk: 20} }

func stateDirectory() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "Application Support", "mini-docker"), nil
}

func configData(options Options) ([]byte, error) {
	if options.CPUs < 1 || options.CPUs > 64 || options.Memory < 1 || options.Memory > 128 || options.Disk < 4 || options.Disk > 1024 {
		return nil, errors.New("machine resources require 1-64 CPUs, 1-128 GiB memory, and 4-1024 GiB disk")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	home, err = filepath.EvalSymlinks(home)
	if err != nil {
		return nil, err
	}
	paths := append([]string{home}, options.Mounts...)
	mounts := []map[string]any{}
	for _, path := range paths {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return nil, err
		}
		absolute, err = filepath.EvalSymlinks(absolute)
		if err != nil {
			return nil, fmt.Errorf("shared directory: %w", err)
		}
		info, err := os.Stat(absolute)
		if err != nil || !info.IsDir() || absolute == "/" {
			return nil, fmt.Errorf("share must be an existing directory other than /: %s", path)
		}
		overlap := false
		for _, mount := range mounts {
			previous := mount["location"].(string)
			if within(previous, absolute) {
				overlap = true
				break
			}
			if within(absolute, previous) {
				return nil, fmt.Errorf("shared directories overlap: %s and %s", previous, absolute)
			}
		}
		if !overlap {
			mounts = append(mounts, map[string]any{"location": absolute, "mountPoint": absolute, "writable": true})
		}
	}
	arch := map[string]string{"arm64": "aarch64", "amd64": "x86_64"}[runtime.GOARCH]
	if arch == "" {
		return nil, errors.New("macOS arm64 and amd64 are supported")
	}
	imageArch := runtime.GOARCH
	data := map[string]any{
		"minimumLimaVersion": "2.0.0", "vmType": "vz", "arch": arch,
		"cpus": options.CPUs, "memory": fmt.Sprintf("%dGiB", options.Memory), "disk": fmt.Sprintf("%dGiB", options.Disk),
		"images":    []map[string]string{{"location": "https://cloud-images.ubuntu.com/releases/24.04/release/ubuntu-24.04-server-cloudimg-" + imageArch + ".img", "arch": arch}},
		"mountType": "virtiofs", "mounts": mounts, "containerd": map[string]bool{"system": false, "user": false},
		"ssh": map[string]any{"forwardAgent": false, "loadDotSSHPubKeys": false, "overVsock": false},
		// Dedicated guest loopback addresses expose only mini-docker's published
		// sockets. Other guest services never match the forwarding rules.
		"portForwards": []map[string]any{
			{"guestIP": "127.0.0.2", "hostIP": "0.0.0.0", "proto": "any"},
			{"guestIP": "127.0.0.3", "hostIP": "127.0.0.1", "proto": "any"},
			{"guestIP": "0.0.0.0", "proto": "any", "ignore": true},
		},
		"provision": []map[string]string{{"mode": "system", "script": provisionScript}},
	}
	return json.MarshalIndent(data, "", "  ")
}

const provisionScript = `#!/bin/sh
set -eu
if [ -f /usr/local/lib/mini-docker/provisioned-v1 ]; then exit 0; fi
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y busybox-static binutils iproute2 nftables util-linux conntrack apparmor-utils
cat > /etc/apparmor.d/mini-docker <<'PROFILE'
abi <abi/4.0>,
profile mini-docker-rootless /usr/local/bin/mdocker flags=(unconfined,attach_disconnected) {
  userns,
}
PROFILE
apparmor_parser -r /etc/apparmor.d/mini-docker
loginctl enable-linger '{{.User}}'
mkdir -p '/etc/systemd/system/user@{{.UID}}.service.d'
printf '[Service]\nDelegate=cpu memory pids\n' > '/etc/systemd/system/user@{{.UID}}.service.d/mini-docker.conf'
systemctl daemon-reload
systemctl restart 'user@{{.UID}}.service'
mkdir -p /usr/local/lib/mini-docker
touch /usr/local/lib/mini-docker/provisioned-v1
`

func within(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (instance Instance) hostPath(path string) (string, error) {
	if path == BuiltinRootFS {
		return guestRootFS, nil
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	absolute, err = filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("host path must be a directory: %s", path)
	}
	for _, mount := range instance.Config.Mounts {
		if within(mount.Location, absolute) && mount.Writable {
			rel, _ := filepath.Rel(mount.Location, absolute)
			point := mount.MountPoint
			if point == "" {
				point = mount.Location
			}
			return filepath.Join(point, rel), nil
		}
	}
	return "", fmt.Errorf("directory is not shared with the machine: %s; stop the machine with mdocker machine stop, add it with mdocker machine share %s, then mdocker machine start (stopping terminates workloads and preserves their files)", absolute, quote(absolute))
}
