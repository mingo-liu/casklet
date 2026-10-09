package cli

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mingo-liu/casklet/internal/container"
)

func TestParseHealthWait(t *testing.T) {
	for _, test := range []struct {
		args    []string
		healthy bool
		timeout time.Duration
	}{
		{[]string{"wait", "worker"}, false, 30 * time.Second},
		{[]string{"wait", "--healthy", "worker"}, true, 30 * time.Second},
		{[]string{"wait", "--healthy", "--timeout", "2m", "worker"}, true, 2 * time.Minute},
		{[]string{"wait", "--timeout=0s", "--healthy", "worker"}, true, 0},
	} {
		r, err := Parse(test.args)
		if err != nil || r.Healthy != test.healthy || r.WaitTimeout != test.timeout || r.Reference != "worker" {
			t.Fatalf("%q: %+v %v", test.args, r, err)
		}
	}
	for _, args := range [][]string{
		{"wait", "--timeout", "1s", "worker"},
		{"wait", "--healthy=false", "--timeout=0s", "worker"},
		{"wait", "--healthy", "--timeout=-1s", "worker"},
		{"wait", "--healthy", "--timeout=soon", "worker"},
		{"wait", "--healthy"},
		{"wait", "worker", "--healthy"},
	} {
		if _, err := Parse(args); err == nil {
			t.Fatalf("accepted %q", args)
		}
	}
}

func TestHealthWaitHelp(t *testing.T) {
	for _, mac := range []bool{false, true} {
		text, err := scopedUsage("wait", mac)
		for _, want := range []string{"--healthy", "--timeout", "default: 30s", "0s", "124", "healthcheck", "observed execution", "does not stop"} {
			if err != nil || !strings.Contains(text, want) {
				t.Fatalf("missing %q: %v\n%s", want, err, text)
			}
		}
	}
	for _, args := range [][]string{{"wait", "--help"}, {"help", "wait"}, {"wait", "--healthy", "--timeout", "1s", "--help"}} {
		r, err := Parse(args)
		if err != nil || r.Action != "help" || r.HelpTopic != "wait" {
			t.Fatalf("%q: %+v %v", args, r, err)
		}
	}
}

func TestHealthWaitTimeoutExitStatus(t *testing.T) {
	var diagnostic bytes.Buffer
	code := manageOperation(Request{Action: "wait", Healthy: true}, &diagnostic, func(context.Context) (int, error) {
		return 0, fmt.Errorf("%w after 30s", container.ErrWaitTimeout)
	})
	if code != 124 || !strings.Contains(diagnostic.String(), "timed out waiting") {
		t.Fatalf("code=%d diagnostic=%q", code, diagnostic.String())
	}
	diagnostic.Reset()
	code = manageOperation(Request{Action: "wait", Healthy: true}, &diagnostic, func(context.Context) (int, error) {
		return 0, context.DeadlineExceeded
	})
	if code != 125 {
		t.Fatalf("unrelated deadline code=%d", code)
	}
}
