package rootfs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mingo-liu/casklet/internal/config"
)

func TestValidateGenericFilesystemAllowsMergedUsrAndMissingMountTargets(t *testing.T) {
	tree := t.TempDir()
	must(t, os.MkdirAll(filepath.Join(tree, "usr/bin"), 0755))
	must(t, os.Symlink("/usr/bin", filepath.Join(tree, "bin")))
	if _, err := Validate(tree); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateBusyBox(tree); err == nil {
		t.Fatal("builtin validation accepted generic filesystem")
	}
	must(t, os.Symlink("/dev", filepath.Join(tree, "dev")))
	if _, err := Validate(tree); err == nil {
		t.Fatal("symlink mount target accepted")
	}
}

func TestPrepareImageWorkdirConfinesAbsoluteSymlinkAndPreservesExistingMode(t *testing.T) {
	tree := t.TempDir()
	must(t, os.Mkdir(filepath.Join(tree, "real"), 0755))
	must(t, os.Symlink("/real", filepath.Join(tree, "work")))
	cfg := config.Config{Workdir: "/work/new", User: &config.User{UID: uint32(os.Getuid()), GID: uint32(os.Getgid())}}
	must(t, PrepareImageWorkdir(tree, cfg))
	info, err := os.Stat(filepath.Join(tree, "real/new"))
	must(t, err)
	if !info.IsDir() {
		t.Fatal("working directory was not created in image root")
	}
	must(t, os.Chmod(filepath.Join(tree, "real/new"), 0700))
	must(t, PrepareImageWorkdir(tree, cfg))
	info, err = os.Stat(filepath.Join(tree, "real/new"))
	must(t, err)
	if info.Mode().Perm() != 0700 {
		t.Fatal("changed existing directory mode")
	}
	must(t, os.WriteFile(filepath.Join(tree, "file"), nil, 0644))
	if err := PrepareImageWorkdir(tree, config.Config{Workdir: "/file"}); err == nil {
		t.Fatal("file working directory accepted")
	}
	must(t, os.Symlink("../outside", filepath.Join(tree, "escape")))
	if err := PrepareImageWorkdir(tree, config.Config{Workdir: "/escape/new"}); err == nil {
		t.Fatal("working directory symlink escape accepted")
	}
}
