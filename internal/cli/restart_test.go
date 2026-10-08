package cli

import (
	"testing"
)

func TestRestartPolicyRequiresDetachedExecution(t *testing.T) {
	for _, policy := range []string{"no", "always", "unless-stopped", "on-failure:2"} {
		r, err := Parse([]string{"run", "-d", "--rootfs", "/template", "--restart", policy, "--", "sh"})
		if err != nil || r.Config.RestartPolicy != policy {
			t.Fatalf("%s %+v %v", policy, r, err)
		}
	}
	for _, args := range [][]string{{"run", "--rootfs", "/template", "--restart", "always", "--", "sh"}, {"run", "-d", "--rootfs", "/template", "--restart", "", "--", "sh"}, {"run", "-d", "--rootfs", "/template", "--restart", "on-failure:0", "--", "sh"}} {
		if _, err := Parse(args); err == nil {
			t.Fatalf("accepted %q", args)
		}
	}
}
