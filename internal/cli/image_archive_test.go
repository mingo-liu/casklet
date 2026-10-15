package cli

import (
	"strings"
	"testing"
)

func TestImageArchiveSyntaxAndDiagnostics(t *testing.T) {
	for _, args := range [][]string{{"image", "load"}, {"image", "save", "sha256:" + strings.Repeat("a", 64)}, {"image", "save", "abc123"}} {
		r, err := Parse(args)
		if err != nil || !strings.HasPrefix(r.Action, "image-") {
			t.Fatalf("%v: %+v %v", args, r, err)
		}
	}
	for _, args := range [][]string{{"image", "load", "file.tar"}, {"image", "load", "--json"}, {"image", "save"}, {"image", "save", "app:latest"}, {"image", "save", "a", "b"}} {
		_, err := Parse(args)
		if err == nil {
			t.Fatalf("accepted %v", args)
		}
		diagnostic := argumentError(args, err).Error()
		if !strings.Contains(diagnostic, "casklet image "+args[1]+" --help") {
			t.Fatalf("unfocused diagnostic: %s", diagnostic)
		}
	}
}
