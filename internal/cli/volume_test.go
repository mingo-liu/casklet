package cli

import (
	"github.com/mingo-liu/casklet/internal/config"
	"testing"
)

func TestVolumeCommandsAndMountValidation(t *testing.T) {
	for _, args := range [][]string{{"volume", "create", "data"}, {"volume", "ls", "--json"}, {"volume", "inspect", "data"}, {"volume", "rm", "data"}, {"run", "--rootfs", "/template", "--mount", "type=volume,source=data,target=/data", "--", "sh"}} {
		if _, err := Parse(args); err != nil {
			t.Fatalf("%q: %v", args, err)
		}
	}
	for _, args := range [][]string{{"volume", "create", "../escape"}, {"volume", "rm"}, {"volume", "ls", "extra"}, {"run", "--rootfs", "/template", "--rootless", "--mount", "type=volume,source=data,target=/data", "--", "sh"}} {
		if _, err := Parse(args); err == nil {
			t.Fatalf("accepted %q", args)
		}
	}
	for _, value := range []string{"type=volume,source=/escape,target=/data", "type=volume,source=data,target=/proc", "type=other,source=data,target=/data"} {
		if _, err := config.ParseMount(value); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
}
func TestNamedVolumesAreNeverTranslatedAsHostPaths(t *testing.T) {
	args := []string{"run", "--mount", "type=volume,source=data,target=/data", "--", "sh"}
	got, err := hostPathArguments(args, "run")
	if err != nil || got[2] != args[2] {
		t.Fatalf("volume translation: %q %v", got, err)
	}
}
