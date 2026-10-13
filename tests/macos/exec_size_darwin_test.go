//go:build darwin

package macos

import (
	"strings"
	"testing"

	"github.com/mingo-liu/casklet/internal/container"
)

func TestLargeInheritedEnvironmentThroughMacExecHealthAndRestart(t *testing.T) {
	id := detached(t, "--env", "LARGE="+strings.Repeat("x", 70<<10), "--health-cmd", `test "${#LARGE}" -eq 71680`, "--health-interval", "100ms", "--health-timeout", "1s", "--health-retries", "1", "--", "/bin/sh", "-c", `test "${#LARGE}" -eq 71680 && exec sleep 300`)
	macHealth(t, id, container.HealthHealthy)
	check := func() {
		t.Helper()
		if out := success(t, "exec", "--env", "EXTRA="+strings.Repeat("y", 4<<10), id, "--", "/bin/sh", "-c", `printf '%s:%s\n' "${#LARGE}" "${#EXTRA}"`); out != "71680:4096\n" {
			t.Fatal(out)
		}
	}
	check()
	success(t, "restart", id)
	macHealth(t, id, container.HealthHealthy)
	check()
}
