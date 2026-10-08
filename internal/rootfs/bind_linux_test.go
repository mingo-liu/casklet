//go:build linux

package rootfs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mingo-liu/casklet/internal/config"
)

func TestMountSourceDirectories(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	if err := os.Mkdir(source, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(source, "child"), 0755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(source, link); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{source, dir} {
		if err := ValidateMountSources([]config.BindMount{{Source: source, Target: "/data"}}, "/template"); err != nil {
			t.Fatal(err)
		}
	}
	for _, source := range []string{link, link + "/child", file, dir + "/missing"} {
		if err := ValidateMountSources([]config.BindMount{{Source: source, Target: "/data"}}, "/template"); err == nil {
			t.Errorf("accepted unsafe source %s", source)
		}
	}
	sources, err := openMountSources([]config.BindMount{{Source: source, Target: "/data"}}, "/template")
	if err != nil {
		t.Fatal(err)
	}
	defer closeMountSources(sources)
	original, err := sources[0].Stat()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(source, source+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(source, 0755); err != nil {
		t.Fatal(err)
	}
	pinned, err := sources[0].Stat()
	if err != nil || !os.SameFile(original, pinned) {
		t.Fatalf("source descriptor followed replacement: %v", err)
	}
}

func TestMountTargetCreationRejectsSymlinksAndFiles(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(dir, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{link, link + "/created", file, file + "/created", "relative", dir + "/../escape"} {
		if f, err := openDirectory(target, true); err == nil {
			f.Close()
			t.Errorf("accepted target %s", target)
		}
	}
	if _, err := os.Stat(filepath.Join(outside, "created")); !os.IsNotExist(err) {
		t.Fatalf("escaped through symlink: %v", err)
	}
	target := filepath.Join(dir, "new", "child")
	f, err := openDirectory(target, true)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if info, err := os.Stat(target); err != nil || !info.IsDir() {
		t.Fatalf("target not created: %v", err)
	}
}
