package config

import (
	"fmt"
	"reflect"
	"testing"
)

func TestParseMount(t *testing.T) {
	got, err := ParseMount("target=/data,readonly,source=/srv/data,type=bind")
	want := BindMount{Source: "/srv/data", Target: "/data", ReadOnly: true}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("mount=%+v error=%v", got, err)
	}
	for _, value := range []string{
		"", "source=/srv/data,target=/data", "type=volume,source=/srv/data,target=/data",
		"type=bind,source=/srv/data,target=/data,unknown", "type=bind,source=/srv/data,target=/data,readonly=true",
		"type=bind,source=/srv/data,target=/data,readonly,readonly", "type=bind,source=/srv/data,source=/other,target=/data",
		"type=bind,source,target=/data", "type=bind,source=,target=/data", "type=bind,source=/srv/data,target",
		"type=bind,source=relative,target=/data", "type=bind,source=/srv/data,target=relative",
	} {
		if _, err := ParseMount(value); err == nil {
			t.Errorf("accepted %q", value)
		}
	}
}

func TestValidateMountPaths(t *testing.T) {
	for _, field := range []string{"source", "target"} {
		for _, value := range []string{"", "/", "relative", "/data/..", "/data/.", "/data/", "/data//child", "/data\x00", "/data\n", "/data\r"} {
			mount := BindMount{Source: "/srv/data", Target: "/data"}
			if field == "source" {
				mount.Source = value
			} else {
				mount.Target = value
			}
			if err := ValidateMounts([]BindMount{mount}, ""); err == nil {
				t.Errorf("accepted %s=%q", field, value)
			}
		}
	}
	for _, source := range []string{"/proc", "/proc/1/root", "/sys", "/dev", "/var", "/var/lib", "/var/lib/casklet/runs", "/template", "/template/data"} {
		if err := ValidateMounts([]BindMount{{Source: source, Target: "/data"}}, "/template"); err == nil {
			t.Errorf("accepted source %q", source)
		}
	}
	for _, target := range []string{"/proc", "/proc/data", "/dev", "/dev/data", "/sys", "/tmp"} {
		if err := ValidateMounts([]BindMount{{Source: "/srv/data", Target: target}}, ""); err == nil {
			t.Errorf("accepted target %q", target)
		}
	}
	if err := ValidateMounts([]BindMount{{Source: "/srv/data", Target: "/tmp/data"}}, "/srv/template"); err != nil {
		t.Fatal(err)
	}
}

func TestMountTargetOverlapAndLimits(t *testing.T) {
	for _, targets := range [][]string{{"/data", "/data"}, {"/data", "/data/child"}, {"/data/child", "/data"}} {
		mounts := []BindMount{{Source: "/srv/a", Target: targets[0]}, {Source: "/srv/b", Target: targets[1]}}
		if err := ValidateMounts(mounts, ""); err == nil {
			t.Errorf("accepted overlapping targets %v", targets)
		}
	}
	mounts := make([]BindMount, 33)
	for i := range mounts {
		mounts[i] = BindMount{Source: "/srv/data", Target: fmt.Sprintf("/data%d", i)}
	}
	if err := ValidateMounts(mounts[:32], ""); err != nil {
		t.Fatal(err)
	}
	if err := ValidateMounts(mounts, ""); err == nil {
		t.Fatal("accepted too many mounts")
	}
	cfg := Config{Command: []string{"sh"}, Mounts: []BindMount{{Source: "/srv/data", Target: "/proc"}}}
	if err := cfg.ValidateExecution(); err == nil {
		t.Fatal("execution validation ignored mounts")
	}
}

func TestMountRejectsMappedRuntimeStorage(t *testing.T) {
	for _, source := range []string{"/tmp", "/tmp/casklet-userns", "/tmp/casklet-userns/run-a/rootfs"} {
		if err := ValidateMounts([]BindMount{{Source: source, Target: "/data"}}, ""); err == nil {
			t.Fatalf("accepted mapped runtime storage %s", source)
		}
	}
}
