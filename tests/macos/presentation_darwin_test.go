//go:build darwin

package macos

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/mingo-liu/mini-docker/internal/config"
	"github.com/mingo-liu/mini-docker/internal/container"
	"github.com/mingo-liu/mini-docker/internal/machine"
)

type hostInspection struct {
	container.Inspection
	GuestResources struct {
		RootFS  string               `json:"rootfs"`
		Mounts  []config.BindMount   `json:"mounts"`
		Publish []config.PortMapping `json:"publish"`
	} `json:"guest_resources"`
}

func inspectHost(t *testing.T, id string) hostInspection {
	t.Helper()
	var inspection hostInspection
	if err := json.Unmarshal([]byte(success(t, "inspect", id)), &inspection); err != nil {
		t.Fatal(err)
	}
	return inspection
}

func TestInspectHostPathsSurviveSourceRemoval(t *testing.T) {
	directory := hostDirectory(t)
	directory, err := filepath.EvalSymlinks(directory)
	if err != nil {
		t.Fatal(err)
	}
	bind := filepath.Join(directory, "data")
	if err := os.Mkdir(bind, 0755); err != nil {
		t.Fatal(err)
	}
	template := filepath.Join(directory, "template")
	success(t, "rootfs", template)
	id := detached(t, "--rootfs", template, "--mount", "type=bind,source="+bind+",target=/data,readonly", "--", "/bin/sleep", "60")
	if err := os.RemoveAll(template); err != nil {
		t.Fatal(err)
	}
	inspection := inspectHost(t, id)
	if inspection.Config.RootFS != template || len(inspection.Config.Mounts) != 1 || inspection.Config.Mounts[0].Source != bind || !inspection.Config.Mounts[0].ReadOnly {
		t.Fatalf("host paths: %+v", inspection)
	}
	if inspection.GuestResources.RootFS == "" || len(inspection.GuestResources.Mounts) != 1 {
		t.Fatalf("missing guest diagnostics: %+v", inspection)
	}
}

func TestInspectBuiltinTemplate(t *testing.T) {
	id := detached(t, "--", "/bin/sleep", "60")
	inspection := inspectHost(t, id)
	if inspection.Config.RootFS != machine.BuiltinRootFS || inspection.GuestResources.RootFS != "/var/lib/mini-docker/templates/busybox" {
		t.Fatalf("builtin template: %+v", inspection)
	}
}
