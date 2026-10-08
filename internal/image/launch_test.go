package image

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mingo-liu/mini-docker/internal/config"
)

func TestLaunchDefaultsAndOverrides(t *testing.T) {
	defaults := LaunchConfig{Entrypoint: []string{"/entry"}, Cmd: []string{"server", "--default"}, Env: []string{"PATH=/usr/local/bin:/usr/bin", "COLOR=blue"}, Workdir: "/data", User: "1000:1001"}
	base := config.Config{Image: "sha256:" + strings.Repeat("a", 64), Env: []string{"COLOR=red"}}
	got, err := defaults.Apply(base, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Command, []string{"/entry", "server", "--default"}) || got.Workdir != "/data" || *got.User != (config.User{UID: 1000, GID: 1001}) {
		t.Fatalf("defaults: %+v", got)
	}
	if env := got.CommandEnvironment(); !reflect.DeepEqual(env, []string{"PATH=/usr/local/bin:/usr/bin", "HOME=/", "LANG=C", "COLOR=red"}) {
		t.Fatalf("env: %v", env)
	}
	base.Command = []string{"alternate"}
	base.Workdir = "/override"
	base.User = &config.User{UID: 12, GID: 13}
	got, err = defaults.Apply(base, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Command, []string{"/entry", "alternate"}) || got.Workdir != "/override" || got.User != base.User {
		t.Fatalf("override: %+v", got)
	}
	for _, entry := range []string{"", "/custom"} {
		got, err = defaults.Apply(base, t.TempDir(), &entry)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"alternate"}
		if entry != "" {
			want = append([]string{entry}, want...)
		}
		if !reflect.DeepEqual(got.Command, want) {
			t.Fatalf("entrypoint %q: %v", entry, got.Command)
		}
	}
	base.Command = nil
	entry := "/custom"
	got, err = defaults.Apply(base, t.TempDir(), &entry)
	if err != nil || !reflect.DeepEqual(got.Command, []string{entry}) {
		t.Fatalf("entrypoint clears Cmd: %v %v", got.Command, err)
	}
	if _, err := (LaunchConfig{}).Apply(base, t.TempDir(), nil); err == nil {
		t.Fatal("missing default command accepted")
	}
}

func TestResolveImageUsers(t *testing.T) {
	tree := t.TempDir()
	if err := os.Mkdir(filepath.Join(tree, "etc"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree, "etc/passwd"), []byte("root:x:0:0:root:/:/bin/sh\napp:x:123:456:app:/data:/bin/sh\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree, "etc/group"), []byte("data:x:789:\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for input, want := range map[string]config.User{"app": {UID: 123, GID: 456}, "123": {UID: 123, GID: 456}, "app:data": {UID: 123, GID: 789}, "123:42": {UID: 123, GID: 42}, "555": {UID: 555, GID: 0}} {
		got, err := resolveUser(tree, input)
		if err != nil || *got != want {
			t.Fatalf("%s = %v %v; want %v", input, got, err, want)
		}
	}
	for _, value := range []string{"missing", "app:missing", "app:", ":123", "123:4:5", "4294967295"} {
		if _, err := resolveUser(tree, value); err == nil {
			t.Fatalf("invalid user %q accepted", value)
		}
	}
}

func TestNormalizeImageReferences(t *testing.T) {
	for input, want := range map[string]string{"redis:8": "index.docker.io/library/redis:8", "nginx": "index.docker.io/library/nginx:latest", "ghcr.io/org/app:v1": "ghcr.io/org/app:v1"} {
		got, err := NormalizeReference(input)
		if err != nil || got != want {
			t.Fatalf("%s = %s %v", input, got, err)
		}
	}
	for _, input := range []string{"", "https://example.com/app", "../outside", "redis:8\n", "sha256:abc"} {
		if _, err := NormalizeReference(input); err == nil {
			t.Fatalf("invalid reference %q accepted", input)
		}
	}
}
