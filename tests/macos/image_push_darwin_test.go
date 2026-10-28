//go:build darwin

package macos

import (
	"strings"
	"testing"

	"github.com/mingo-liu/casklet/internal/image"
	"github.com/mingo-liu/casklet/internal/registryauth"
)

func TestImageTagPushPrivateRegistry(t *testing.T) {
	client(t)
	fixture := progressRegistryDetails(t, true)
	registry, err := registryauth.Registry(fixture.References[0])
	if err != nil {
		t.Fatal(err)
	}
	store, err := registryauth.OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Delete(registry) })
	_, diagnostic, code := registryInputCommand(t, "casklet-fixture-token\n", "login", "-u", "casklet-test", "--password-stdin", registry)
	if code != 0 {
		t.Fatal(diagnostic)
	}
	id := strings.TrimSpace(success(t, "image", "pull", fixture.References[0]))
	t.Cleanup(func() { command(t, "image", "rm", id) })
	target := registry + "/casklet/pushed:v1"
	success(t, "image", "tag", id, target)
	digest := strings.TrimSpace(success(t, "image", "push", target, target))
	if image.ValidateID(digest) != nil {
		t.Fatalf("push digest: %q", digest)
	}
	pulled := strings.TrimSpace(success(t, "image", "pull", target))
	if pulled != id {
		t.Fatalf("push/pull changed content identity: %s != %s", pulled, id)
	}
	if got := success(t, "run", "--image", target); got != "image-progress-ok\n" {
		t.Fatalf("defaults lost: %q", got)
	}
	success(t, "logout", registry)
	_, diagnostic, code = command(t, "image", "push", id, target)
	if code == 0 {
		t.Fatal("anonymous push to private registry succeeded")
	}
	_, diagnostic, code = registryInputCommand(t, "wrong-token\n", "login", "-u", "casklet-test", "--password-stdin", registry)
	if code != 0 {
		t.Fatal(diagnostic)
	}
	_, diagnostic, code = command(t, "image", "push", id, target)
	if code == 0 || !strings.Contains(diagnostic, "authenticated registry push failed") || strings.Contains(diagnostic, "casklet-fixture-token") || strings.Contains(diagnostic, "wrong-token") {
		t.Fatalf("push failure: %d %s", code, diagnostic)
	}
}
