//go:build linux

package integration

import (
	"strings"
	"testing"
)

func TestImageTagPushRoundTrip(t *testing.T) {
	require(t)
	ref, closeRegistry := ociRegistryFixture(t)
	defer closeRegistry()
	id := strings.TrimSpace(backgroundSuccess(t, "image", "pull", ref))
	t.Cleanup(func() { backgroundCLI(t, "image", "rm", id) })
	target := ref + "-pushed"
	backgroundSuccess(t, "image", "tag", id, target)
	digest := strings.TrimSpace(backgroundSuccess(t, "image", "push", target, target))
	if !strings.HasPrefix(digest, "sha256:") || len(digest) != 71 {
		t.Fatalf("digest: %q", digest)
	}
	if got := strings.TrimSpace(backgroundSuccess(t, "image", "pull", target)); got != id {
		t.Fatalf("identity changed: %s", got)
	}
	if got := backgroundSuccess(t, "run", "--image", target); got != "image:default:/new-work\n" {
		t.Fatalf("defaults: %q", got)
	}
}
