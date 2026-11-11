package rootfs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigureDNSReplacesRegularResolver(t *testing.T) {
	root, secret := resolverFixture(t)
	must(t, os.WriteFile(filepath.Join(root, "etc/resolv.conf"), []byte("old"), 0644))
	must(t, ConfigureDNS(root, []string{"8.8.8.8", "1.1.1.1"}))
	assertResolver(t, root)
	assertSecretUnchanged(t, secret)
}

func TestConfigureDNSReplacesResolverSymlink(t *testing.T) {
	root, secret := resolverFixture(t)
	must(t, os.Symlink(secret, filepath.Join(root, "etc/resolv.conf")))
	must(t, ConfigureDNS(root, []string{"8.8.8.8", "1.1.1.1"}))
	assertResolver(t, root)
	assertSecretUnchanged(t, secret)
}

func TestConfigureDNSReplacesResolverHardlink(t *testing.T) {
	root, secret := resolverFixture(t)
	must(t, os.Link(secret, filepath.Join(root, "etc/resolv.conf")))
	must(t, ConfigureDNS(root, []string{"8.8.8.8", "1.1.1.1"}))
	assertResolver(t, root)
	assertSecretUnchanged(t, secret)
}

func TestConfigureDNSReclaimsStaleResolverStaging(t *testing.T) {
	root, secret := resolverFixture(t)
	must(t, os.Symlink(secret, filepath.Join(root, "etc/.casklet-resolv")))
	must(t, ConfigureDNS(root, []string{"8.8.8.8", "1.1.1.1"}))
	assertResolver(t, root)
	assertSecretUnchanged(t, secret)
}

func TestConfigureDNSRejectsEscapingDirectory(t *testing.T) {
	root, secret := resolverFixture(t)
	must(t, os.Remove(filepath.Join(root, "etc")))
	must(t, os.Symlink(filepath.Dir(secret), filepath.Join(root, "etc")))
	if err := ConfigureDNS(root, []string{"8.8.8.8", "1.1.1.1"}); err == nil {
		t.Fatal("accepted escaping resolver directory")
	}
	assertSecretUnchanged(t, secret)
}

func TestConfigureDNSRejectsResolverDirectory(t *testing.T) {
	root, secret := resolverFixture(t)
	must(t, os.Mkdir(filepath.Join(root, "etc/resolv.conf"), 0755))
	if err := ConfigureDNS(root, []string{"8.8.8.8", "1.1.1.1"}); err == nil {
		t.Fatal("accepted directory as resolver file")
	}
	assertSecretUnchanged(t, secret)
}

func resolverFixture(t *testing.T) (string, string) {
	t.Helper()
	root, outside := t.TempDir(), t.TempDir()
	secret := filepath.Join(outside, "secret")
	must(t, os.WriteFile(secret, []byte("unchanged"), 0644))
	must(t, os.Mkdir(filepath.Join(root, "etc"), 0755))
	return root, secret
}

func assertResolver(t *testing.T, root string) {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(root, "etc/resolv.conf"))
	if err != nil || !strings.Contains(string(contents), "nameserver 8.8.8.8\nnameserver 1.1.1.1\n") {
		t.Fatalf("contents=%q error=%v", contents, err)
	}
}

func assertSecretUnchanged(t *testing.T, secret string) {
	t.Helper()
	contents, err := os.ReadFile(secret)
	if err != nil || string(contents) != "unchanged" {
		t.Fatalf("outside changed: %q %v", contents, err)
	}
}
