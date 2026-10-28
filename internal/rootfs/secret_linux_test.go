//go:build linux

package rootfs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mingo-liu/casklet/internal/config"
	"golang.org/x/sys/unix"
)

func TestSecretSourcesAreBoundedRegularFilesAndIndependentSnapshots(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "token")
	value := []byte("private-token\x00\n")
	if err := os.WriteFile(source, value, 0600); err != nil {
		t.Fatal(err)
	}
	specs := []config.Secret{{Source: source, Target: "token"}}
	data, err := readSecrets(specs)
	if err != nil || string(data[0]) != string(value) {
		t.Fatalf("read: %v", err)
	}
	os.WriteFile(source, []byte("updated"), 0600)
	if string(data[0]) != string(value) {
		t.Fatal("snapshot changed")
	}
	symlink := filepath.Join(dir, "link")
	os.Symlink(source, symlink)
	fifo := filepath.Join(dir, "fifo")
	unix.Mkfifo(fifo, 0600)
	oversized := filepath.Join(dir, "large")
	os.WriteFile(oversized, make([]byte, config.MaxSecretBytes+1), 0600)
	for _, path := range []string{dir, symlink, fifo, oversized, filepath.Join(dir, "missing")} {
		if err := ValidateSecretSources([]config.Secret{{Source: path, Target: "bad"}}); err == nil || strings.Contains(err.Error(), string(value)) {
			t.Fatalf("unsafe source: %s %v", path, err)
		}
	}
}
