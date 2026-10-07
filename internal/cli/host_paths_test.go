package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestHostPathsPreserveWorkloadAndOptionValues(t *testing.T) {
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"run", "--rootfs=" + directory, "--env", "VALUE=--mount", "--mount", "type=bind,source=" + directory + ",target=/data,readonly", "--", "sh", "--rootfs", "relative", "$(touch forbidden)"}
	got, err := hostPathArguments(args, "run")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, args) {
		t.Fatalf("arguments changed: %q", got)
	}
	got, err = hostPathArguments([]string{"image", "import", "."}, "image-import")
	if err != nil || !filepath.IsAbs(got[2]) {
		t.Fatalf("image path: %q, %v", got, err)
	}
}

func TestHostMountResolvesMacSymlinks(t *testing.T) {
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "link")
	if err := os.Symlink(directory, link); err != nil {
		t.Fatal(err)
	}
	got, err := hostPathArguments([]string{"run", "--rootfs", directory, "--mount=type=bind,source=" + link + ",target=/data", "--", "true"}, "run")
	if err != nil {
		t.Fatal(err)
	}
	if got[3] != "--mount=type=bind,source="+directory+",target=/data" {
		t.Fatalf("mount: %q", got[3])
	}
}
