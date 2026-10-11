//go:build linux

package integration

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mingo-liu/casklet/internal/image"
	"github.com/mingo-liu/casklet/internal/registryauth"
)

func TestCredentialPreparationPullCacheAndWorkloadSeparation(t *testing.T) {
	reference, closeRegistry := ociRegistryFixtureWithAuth(t, true)
	registry, err := registryauth.Registry(reference)
	if err != nil {
		t.Fatal(err)
	}
	credential := registryauth.Credential{Registry: registry, Username: "casklet-test", Password: "wrong-token"}
	encoded, _ := json.Marshal(credential)
	code, out, diagnostic := start(t, string(encoded), "__credential-prepare", "pull", "plain", reference).wait(t)
	if code == 0 || out != registryauth.RequestMarker || !strings.Contains(diagnostic, "authenticated registry pull failed") || strings.Contains(diagnostic, "wrong-token") || strings.Contains(diagnostic, "casklet-fixture-token") {
		t.Fatalf("failed authentication: %d %q %q", code, out, diagnostic)
	}
	credential.Password = "casklet-fixture-token"
	encoded, _ = json.Marshal(credential)
	code, out, diagnostic = start(t, string(encoded), "__credential-prepare", "resolve", "plain", reference).wait(t)
	id := strings.TrimSpace(strings.TrimPrefix(out, registryauth.RequestMarker))
	if code != 0 || !strings.HasPrefix(out, registryauth.RequestMarker) || image.ValidateID(id) != nil {
		t.Fatalf("authenticated prepare: %d %q %q", code, out, diagnostic)
	}
	t.Cleanup(func() { start(t, "", "image", "rm", id).wait(t) })
	closeRegistry()
	code, out, diagnostic = start(t, "", "__credential-prepare", "resolve", "plain", reference).wait(t)
	if code != 0 || out != id+"\n" {
		t.Fatalf("offline resolve requested credentials: %d %q %q", code, out, diagnostic)
	}
	code, out, diagnostic = start(t, "workload-only\n", "run", "--image", id, "--entrypoint", "", "--", "/bin/cat").wait(t)
	if code != 0 || out != "workload-only\n" || strings.Contains(diagnostic, "casklet-fixture-token") {
		t.Fatalf("workload input/config separation: %d %q %q", code, out, diagnostic)
	}
}
