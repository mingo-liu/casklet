package main

import "testing"

func TestRunRejectsInvalidSupervisorGeneration(t *testing.T) {
	tests := []string{"", "-1", "invalid", "18446744073709551616"}
	for _, tt := range tests {
		t.Run(tt, func(t *testing.T) {
			if got := run([]string{"__supervise", "unused", tt}); got != 125 {
				t.Fatalf("supervisor generation %q: exit status = %d; want 125", tt, got)
			}
		})
	}
}
