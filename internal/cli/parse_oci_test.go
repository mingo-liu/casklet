package cli

import (
	"strings"
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

func TestParseShortIDCommands(t *testing.T) {
	for _, ref := range []string{"a", "406e742c72ac", strings.Repeat("a", 64), "sha256:406e742c72ac"} {
		for _, args := range [][]string{{"image", "rm", ref}, {"run", "--image", ref, "--", "sh"}} {
			if _, err := Parse(args); err != nil {
				t.Errorf("short image reference %v rejected: %v", args, err)
			}
		}
	}
	for _, cmd := range []string{"inspect", "stats", "logs", "stop", "start", "restart", "wait", "rm"} {
		if _, err := Parse([]string{cmd, "406e742c72ac"}); err != nil {
			t.Errorf("short container reference %s rejected: %v", cmd, err)
		}
	}
	if _, err := Parse([]string{"exec", "406e742c72ac", "--", "sh"}); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"", "sha256:", "sha256:xyz", "ABC", "redis:8", "../abc", strings.Repeat("a", 65)} {
		if _, err := Parse([]string{"image", "rm", ref}); err == nil {
			t.Errorf("invalid image ID accepted: %q", ref)
		}
	}
}
