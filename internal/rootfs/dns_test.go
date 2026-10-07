package rootfs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigureDNSConfinesWrites(t *testing.T) {
	for _, kind := range []string{"regular", "symlink", "hardlink", "escaping-etc", "directory", "stale-stage"} {
		t.Run(kind, func(t *testing.T) {
			root, outside := t.TempDir(), t.TempDir()
			secret := filepath.Join(outside, "secret")
			if err := os.WriteFile(secret, []byte("unchanged"), 0644); err != nil {
				t.Fatal(err)
			}
			if kind == "escaping-etc" {
				if err := os.Symlink(outside, filepath.Join(root, "etc")); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Mkdir(filepath.Join(root, "etc"), 0755); err != nil {
					t.Fatal(err)
				}
				resolver := filepath.Join(root, "etc/resolv.conf")
				var err error
				switch kind {
				case "symlink":
					err = os.Symlink(secret, resolver)
				case "hardlink":
					err = os.Link(secret, resolver)
				case "directory":
					err = os.Mkdir(resolver, 0755)
				case "stale-stage":
					err = os.Symlink(secret, filepath.Join(root, "etc/.mini-docker-resolv"))
				default:
					err = os.WriteFile(resolver, []byte("old"), 0644)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			err := ConfigureDNS(root, []string{"8.8.8.8", "1.1.1.1"})
			shouldFail := kind == "escaping-etc" || kind == "directory"
			if (err != nil) != shouldFail {
				t.Fatalf("configure: %v", err)
			}
			if !shouldFail {
				contents, err := os.ReadFile(filepath.Join(root, "etc/resolv.conf"))
				if err != nil || !strings.Contains(string(contents), "nameserver 8.8.8.8\nnameserver 1.1.1.1\n") {
					t.Fatalf("contents=%q error=%v", contents, err)
				}
			}
			contents, err := os.ReadFile(secret)
			if err != nil || string(contents) != "unchanged" {
				t.Fatalf("outside changed: %q %v", contents, err)
			}
		})
	}
}
