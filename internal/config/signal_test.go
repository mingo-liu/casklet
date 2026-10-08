package config

import (
	"testing"
)

func TestStopSignalsUseLinuxNumbersOnEveryHost(t *testing.T) {
	for value, want := range map[string]int{"SIGTERM": 15, "TERM": 15, "sigusr1": 10, "SIGUSR2": 12, "BUS": 7, "SIGSTOP": 19, "64": 64, "9": 9} {
		got, err := ParseStopSignal(value)
		if err != nil || got != want {
			t.Fatalf("%s: %d %v", value, got, err)
		}
	}
	for _, value := range []string{"", "0", "65", "-1", "garbage", " TERM", "SIG", "9\n"} {
		if _, err := ParseStopSignal(value); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
	if (Config{}).StoppingSignal() != 15 {
		t.Fatal("wrong default")
	}
}
