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
	var resize bytes.Buffer
	if err := HostCommand(context.Background(), []string{"machine", "resize", "--memory=8", "--help"}, &resize, io.Discard); err != nil || resize.String() != hostHelp["machine resize"] {
		t.Fatalf("resize help after options: %s %v", &resize, err)
	}
}

func TestHostHelpRejectsUnknownAndExtraTopicsWithoutLima(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, args := range [][]string{
		{"machine", "unknown", "--help"}, {"machine", "--help", "extra"},
		{"machine", "share", "--help", "extra"}, {"rootfs", "--help", "extra"},
		{"machine", "init", "--cpus", "2", "--help", "extra"},
		{"machine", "resize", "--cpus", "2", "--help", "extra"},
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
		{"machine", "resize", "--cpus", "-h"},
		{"rootfs", "./--help"},
	} {
		if _, err := parseHostCommand(args); errors.Is(err, flag.ErrHelp) {
			t.Errorf("literal value triggered help: %q", args)
		}
	}
}

func TestMachineStartHelpDocumentsInitializationRecovery(t *testing.T) {
	text, err := Help("machine start")
	if err != nil {
		t.Fatal(err)
	}
	for _, phrase := range []string{"already running", "restore network access and retry", "VM data is preserved"} {
		if !strings.Contains(text, phrase) {
			t.Errorf("missing recovery guidance %q: %s", phrase, text)
		}
	}
}
