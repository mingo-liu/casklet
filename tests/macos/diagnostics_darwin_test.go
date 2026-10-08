//go:build darwin

package macos

import (
	"strings"
	"testing"
)

func TestActionableContainerErrors(t *testing.T) {
	id := detached(t, "--", "/bin/sleep", "60")
	out, diagnostic, code := command(t, "rm", id)
	if code != 125 || out != "" || !strings.Contains(diagnostic, "casklet stop '"+id+"'") || !strings.Contains(diagnostic, "casklet rm '"+id+"'") {
		t.Fatalf("removal hint: %d %q %q", code, out, diagnostic)
	}
	out, diagnostic, code = command(t, "inspect", "ux-error-container-does-not-exist")
	if code != 125 || out != "" || !strings.Contains(diagnostic, "casklet ps -a") {
		t.Fatalf("lookup hint: %d %q %q", code, out, diagnostic)
	}
}
