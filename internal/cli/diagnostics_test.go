package cli

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/mingo-liu/mini-docker/internal/container"
	"github.com/mingo-liu/mini-docker/internal/image"
)

func TestArgumentErrorsGiveScopedHelpAndSeparatorExamples(t *testing.T) {
	for _, tt := range []struct {
		args  []string
		hints []string
	}{
		{[]string{"run", "--rootfs", "/template", "echo", "hello"}, []string{"-- /bin/echo hello", "mdocker run --help"}},
		{[]string{"exec", "worker"}, []string{"mdocker exec worker -- /bin/echo hello", "mdocker exec --help"}},
		{[]string{"logs", "worker", "--tail", "20"}, []string{"flags must precede", "mdocker logs --help"}},
		{[]string{"image", "import"}, []string{"mdocker image import --help"}},
		{[]string{"unknown"}, []string{"mdocker help"}},
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
		{container.ErrNotFound, Request{Action: "inspect", Reference: "worker"}, "mdocker ps -a"},
		{container.ErrNotTerminal, Request{Action: "rm", Reference: "worker"}, "mdocker stop 'worker', then retry mdocker rm 'worker'"},
		{container.ErrNameInUse, Request{Action: "run", Name: "worker"}, "mdocker inspect 'worker'"},
		{image.ErrNotFound, Request{Action: "image-rm"}, "mdocker image ls"},
		{image.ErrInUse, Request{Action: "image-rm"}, "mdocker inspect NAME"},
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
