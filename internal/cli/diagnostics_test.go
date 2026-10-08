package cli

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/mingo-liu/casklet/internal/container"
	"github.com/mingo-liu/casklet/internal/image"
)

func TestArgumentErrorsGiveScopedHelpAndSeparatorExamples(t *testing.T) {
	for _, tt := range []struct {
		args  []string
		hints []string
	}{
		{[]string{"run", "--rootfs", "/template", "echo", "hello"}, []string{"-- /bin/echo hello", "casklet run --help"}},
		{[]string{"exec", "worker"}, []string{"casklet exec worker -- /bin/echo hello", "casklet exec --help"}},
		{[]string{"logs", "worker", "--tail", "20"}, []string{"flags must precede", "casklet logs --help"}},
		{[]string{"image", "import"}, []string{"casklet image import --help"}},
		{[]string{"unknown"}, []string{"casklet help"}},
	} {
		_, err := Parse(tt.args)
		if err == nil {
			t.Fatalf("accepted %q", tt.args)
		}
		for _, hint := range tt.hints {
			if !strings.Contains(err.Error(), hint) {
				t.Fatalf("%q: %v lacks %q", tt.args, err, hint)
			}
		}
	}
}

func TestOperationHintsPreserveErrorIdentity(t *testing.T) {
	for _, tt := range []struct {
		cause   error
		request Request
		hint    string
	}{
		{container.ErrNotFound, Request{Action: "inspect", Reference: "worker"}, "casklet ps -a"},
		{container.ErrNotTerminal, Request{Action: "rm", Reference: "worker"}, "casklet stop 'worker', then retry casklet rm 'worker'"},
		{container.ErrNameInUse, Request{Action: "run", Name: "worker"}, "casklet inspect 'worker'"},
		{image.ErrNotFound, Request{Action: "image-rm"}, "casklet image ls"},
		{image.ErrInUse, Request{Action: "image-rm"}, "casklet inspect NAME"},
	} {
		wrapped := fmt.Errorf("operation failed: %w", tt.cause)
		got := operationError(tt.request, wrapped)
		if !errors.Is(got, tt.cause) || !strings.Contains(got.Error(), tt.hint) {
			t.Fatalf("hint: %v", got)
		}
	}
	original := errors.New("unclassified transport failure")
	if got := operationError(Request{}, original); got != original {
		t.Fatalf("changed unrelated error: %v", got)
	}
}
