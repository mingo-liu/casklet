package cli

import (
	"testing"
)

func TestParseRegistryRunAndImageDefaults(t *testing.T) {
	for _, args := range [][]string{
		{"run", "-d", "--name", "redis", "--image", "redis:8"},
		{"run", "-d", "--name", "redis", "--network", "bridge", "-p", "127.0.0.1:6379:6379", "--image", "redis:8"},
		{"run", "--image", "nginx", "--entrypoint", "", "--", "/bin/sh"},
		{"image", "pull", "redis:8"},
	} {
		if _, err := Parse(args); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	r, err := Parse([]string{"run", "--image", "redis:8"})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Config.Command) != 0 || r.Config.Workdir != "" || r.Config.User != nil {
		t.Fatalf("parser replaced unresolved defaults: %+v", r.Config)
	}
	for _, args := range [][]string{{"run", "--image", "redis:8", "--"}, {"run", "--image", "redis:8", "--workdir", ""}, {"run", "--rootfs", "/tree", "--entrypoint", "/bin/sh", "--", "true"}, {"image", "pull"}, {"image", "pull", "redis:8", "extra"}, {"image", "pull", "sha256:abc"}} {
		if _, err := Parse(args); err == nil {
			t.Fatalf("invalid options accepted: %v", args)
		}
	}
}
