package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestConfigRelativeBindSourcesResolveAgainstFile(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "configs")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(base)
	mounts := []string{"type=bind,source=../data with spaces,target=/data,readonly", "type=bind,source=/srv/absolute,target=/absolute"}
	for _, projectMode := range []bool{false, true} {
		run := map[string]any{"rootfs": "template", "mount": mounts, "command": []string{"/bin/true"}}
		var doc any = run
		args := []string{"run", "--config", "configs/run.json"}
		if projectMode {
			doc = map[string]any{"name": "demo", "volumes": []string{"saved"}, "services": map[string]any{"app": map[string]any{"run": run}}}
			run["mount"] = append(append([]string(nil), mounts...), "type=volume,source=saved,target=/saved")
			args = []string{"project", "up", "--config", "configs/run.json"}
		}
		data, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "run.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
		request, err := Parse(args)
		if err != nil {
			t.Fatalf("project=%v: %v", projectMode, err)
		}
		if projectMode {
			request, err = Parse(request.Project.Services[0].Args)
			if err != nil {
				t.Fatal(err)
			}
		}
		if request.Config.Mounts[0].Source != filepath.Join(base, "data with spaces") || !request.Config.Mounts[0].ReadOnly || request.Config.Mounts[1].Source != "/srv/absolute" {
			t.Fatalf("incorrect expanded mounts: %+v", request.Config.Mounts)
		}
		if projectMode && request.Config.Mounts[2].Source != "cp-demo-saved" {
			t.Fatal("volume name was resolved as a path")
		}
	}
}

func TestConfigRelativeBindStillRejectsUnsafeMounts(t *testing.T) {
	for _, mount := range []string{"type=bind,source=,target=/data", "type=bind,source=./data,target=relative", "type=bind,source=../../proc,target=/data", "type=bind,source=./data,target=/proc", "type=bind,source=./data,source=./other,target=/data"} {
		run := map[string]any{"rootfs": "/template", "mount": []string{mount}, "command": []string{"true"}}
		data, _ := json.Marshal(run)
		if _, _, err := configDataArguments(data, "/work/config/run.json", map[string]bool{}); err == nil {
			t.Fatalf("accepted unsafe mount: %s", mount)
		}
	}
	file := projectConfigFile(t, `{}`)
	protected, err := filepath.Rel(filepath.Dir(file), "/proc")
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{"", protected} {
		run := map[string]any{"rootfs": "/template", "mount": []string{"type=bind,source=" + source + ",target=/data"}, "command": []string{"true"}}
		doc, _ := json.Marshal(map[string]any{"name": "demo", "services": map[string]any{"app": map[string]any{"run": run}}})
		if err := os.WriteFile(file, doc, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Parse([]string{"project", "up", "--config", file}); err == nil {
			t.Fatalf("accepted unsafe project source %q", source)
		}
	}
	data := []byte(`{"rootfs":"data","mount":["type=bind,source=./data/child,target=/data"],"command":["true"]}`)
	options, command, err := configDataArguments(data, filepath.Join(t.TempDir(), "run.json"), map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	args := append(append([]string{"run"}, options...), "--")
	if _, err := Parse(append(args, command...)); err == nil {
		t.Fatal("accepted a source overlapping the rootfs")
	}
}
