package cli

import (
	"testing"
)

func TestStopSignalParsing(t *testing.T) {
	for _, signal := range []string{"SIGUSR1", "10", "SIGKILL"} {
		r, err := Parse([]string{"run", "-d", "--rootfs", "/template", "--stop-signal", signal, "--", "sh"})
		if err != nil || r.Config.StopSignal != signal {
			t.Fatalf("%s: %+v %v", signal, r, err)
		}
	}
	for _, signal := range []string{"", "0", "65", "INVALID"} {
		if _, err := Parse([]string{"run", "--rootfs", "/template", "--stop-signal", signal, "--", "sh"}); err == nil {
			t.Fatalf("accepted %q", signal)
		}
	}
}
