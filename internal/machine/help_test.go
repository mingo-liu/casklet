package machine

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"io"
	"strings"
	"testing"
)

func TestScopedHostHelpDoesNotOpenLimaOrLocalState(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for topic, want := range hostHelp {
		for _, option := range []string{"-h", "--help"} {
			args := append(strings.Fields(topic), option)
			var output bytes.Buffer
			if err := HostCommand(context.Background(), args, &output, io.Discard); err != nil || output.String() != want {
				t.Errorf("%q: %q %v", args, output.String(), err)
			}
		}
	}
	for _, args := range [][]string{
		{"machine", "init", "--cpus", "2", "--help"},
		{"machine", "init", "--mount", "--help", "--memory=2", "-h"},
	} {
		var output bytes.Buffer
		if err := HostCommand(context.Background(), args, &output, io.Discard); err != nil || output.String() != hostHelp["machine init"] {
			t.Errorf("help after options %q: %q %v", args, output.String(), err)
		}
	}
}

func TestHostHelpRejectsUnknownAndExtraTopicsWithoutLima(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, args := range [][]string{
		{"machine", "unknown", "--help"}, {"machine", "--help", "extra"},
		{"machine", "share", "--help", "extra"}, {"rootfs", "--help", "extra"},
		{"machine", "init", "--cpus", "2", "--help", "extra"},
	} {
		var output bytes.Buffer
		err := HostCommand(context.Background(), args, &output, io.Discard)
		if err == nil || output.Len() != 0 || !strings.Contains(err.Error(), "Hint:") || strings.Contains(err.Error(), "Lima is required") {
			t.Errorf("invalid help %q: %q %v", args, output.String(), err)
		}
	}
	if text, err := Help("machine unknown"); err == nil || text != "" {
		t.Fatal("unknown host help topic was accepted")
	}
}

func TestHostHelpDoesNotInterpretOptionOrDirectoryValues(t *testing.T) {
	for _, args := range [][]string{
		{"machine", "init", "--mount", "--help"},
		{"machine", "init", "--cpus", "-h"},
		{"rootfs", "./--help"},
	} {
		if _, err := parseHostCommand(args); errors.Is(err, flag.ErrHelp) {
			t.Errorf("literal value triggered help: %q", args)
		}
	}
}
