//go:build linux

package integration

import (
	"strings"
	"testing"

	"github.com/mingo-liu/casklet/internal/container"
)

func TestLargeInheritedEnvironmentSupportsExecHealthAndRestart(t *testing.T) {
	require(t)
	large := strings.Repeat("x", 70<<10)
	id := startBackground(t, backgroundName(t), []string{"--env", "LARGE=" + large, "--health-cmd", `test "${#LARGE}" -eq 71680`, "--health-interval", "50ms", "--health-timeout", "1s", "--health-retries", "1"}, "/bin/sh", "-c", `test "${#LARGE}" -eq 71680 && exec sleep 300`)
	awaitHealth(t, id, container.HealthHealthy)
	check := func() {
		t.Helper()
		if out := backgroundSuccess(t, "exec", "--env", "EXTRA="+strings.Repeat("y", 4<<10), id, "--", "/bin/sh", "-c", `printf '%s:%s\n' "${#LARGE}" "${#EXTRA}"`); out != "71680:4096\n" {
			t.Fatal(out)
		}
	}
	check()
	backgroundSuccess(t, "restart", id)
	awaitHealth(t, id, container.HealthHealthy)
	check()
}
