//go:build darwin

package macos

import (
	"fmt"
	"strings"
	"testing"
)

func TestLogTailSelectsRotatedSuffixAndEmptyHistory(t *testing.T) {
	id := detached(t, "--log-max-size", "1k", "--log-max-files", "3", "--", "/bin/sh", "-c", `i=0; while [ "$i" -lt 5 ]; do printf '%0900d\n' "$i"; i=$((i+1)); done; printf final`)
	success(t, "wait", id)
	if got := success(t, "logs", "--tail", "2", id); got != fmt.Sprintf("%0900d\nfinal", 4) {
		t.Fatalf("rotated tail=%q", got)
	}
	if got := success(t, "logs", "--tail", "0", id); got != "" {
		t.Fatalf("zero tail=%q", got)
	}
	if got := success(t, "logs", "--tail", "10", id); !strings.HasSuffix(got, fmt.Sprintf("%0900d\nfinal", 4)) || len(got) > 3072 {
		t.Fatalf("retained tail size=%d", len(got))
	}
}
