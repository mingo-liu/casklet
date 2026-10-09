//go:build linux

package integration

import (
	"strings"
	"testing"
)

func TestShortIDImageAndContainerLifecycle(t *testing.T) {
	require(t)
	imageID := importImage(t, imageTemplate(t))
	shortImage := strings.TrimPrefix(imageID, "sha256:")[:12]
	if out := imageSuccess(t, shortImage, nil, "/bin/echo", "short-image"); out != "short-image\n" {
		t.Fatalf("short image execution: %q", out)
	}
	id := startImageContainer(t, backgroundName(t), shortImage, "/bin/sh", "-c", "echo short-ready; sleep 300")
	short := id[:12]
	if got := inspectBackground(t, short); got.ID != id {
		t.Fatalf("inspect did not retain full identity: %+v", got)
	}
	if got := statsBackground(t, short); got.ID != id {
		t.Fatalf("stats did not retain full identity: %+v", got)
	}
	if out := backgroundSuccess(t, "exec", short, "--", "/bin/echo", "short-exec"); out != "short-exec\n" {
		t.Fatalf("short exec: %q", out)
	}
	if out := backgroundSuccess(t, "logs", short); !strings.Contains(out, "short-ready") {
		t.Fatalf("short logs: %q", out)
	}
	assertImageInUse(t, shortImage)
	if code, _, stderr := backgroundCLI(t, "rm", short); code != 125 || !strings.Contains(stderr, "stopped") {
		t.Fatalf("short ID bypassed stopped requirement: %d %q", code, stderr)
	}
	for _, action := range []string{"restart", "stop", "start", "stop"} {
		if out := backgroundSuccess(t, action, short); strings.TrimSpace(out) != id {
			t.Fatalf("%s did not return full ID: %q", action, out)
		}
	}
	if code, _, stderr := backgroundCLI(t, "wait", short); code == 125 || strings.Contains(stderr, "not found") {
		t.Fatalf("short wait failed lookup: %d %q", code, stderr)
	}
	assertImageInUse(t, shortImage)
	backgroundSuccess(t, "rm", short)
	backgroundSuccess(t, "image", "rm", shortImage)
	if code, _, stderr := backgroundCLI(t, "inspect", short); code != 125 || !strings.Contains(stderr, "container not found") {
		t.Fatalf("removed short ID lookup: %d %q", code, stderr)
	}
}
