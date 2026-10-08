package cli

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mingo-liu/casklet/internal/image"
)

func TestProgressOptionsAndGuestModePreserveCommands(t *testing.T) {
	for _, args := range [][]string{
		{"image", "pull", "redis:8"},
		{"image", "pull", "--progress", "auto", "--", "redis:8"},
		{"image", "pull", "--progress=tty", "redis:8"},
		{"run", "--progress=plain", "--env", "VALUE=--progress=tty", "--image", "redis:8", "--", "redis-server", "--progress=auto"},
		{"run", "--image", "redis:8"},
	} {
		r, err := Parse(args)
		if err != nil {
			t.Fatal(err)
		}
		forwarded := guestProgressArguments(args, r, &bytes.Buffer{})
		guest, err := Parse(forwarded)
		if err != nil {
			t.Fatalf("forwarded %q: %v", forwarded, err)
		}
		want := r
		if want.Progress == "auto" {
			want.Progress = "plain"
		}
		if !reflect.DeepEqual(guest, want) {
			t.Fatalf("forwarding changed request: %+v, want %+v", guest, want)
		}
	}
	for _, args := range [][]string{
		{"image", "pull", "--progress=bad", "redis:8"},
		{"run", "--progress=bad", "--image", "redis:8"},
		{"image", "rm", "--progress=plain", "sha256:" + strings.Repeat("a", 64)},
	} {
		if _, err := Parse(args); err == nil {
			t.Fatalf("accepted %q", args)
		}
	}
}

func TestPlainProgressHasNoTerminalControlsAndBoundsUpdates(t *testing.T) {
	var output bytes.Buffer
	p := newPullProgress(&output, "auto")
	now := time.Unix(1, 0)
	p.now = func() time.Time { return now }
	event := image.Progress{Stage: image.ProgressDownloading, Layer: "sha256:" + strings.Repeat("a", 64), Index: 1, Current: 512, Total: 1024}
	p.observe(event)
	for range 100 {
		p.observe(event)
	}
	now = now.Add(time.Second)
	event.Current = 1024
	p.observe(event)
	event.Stage = image.ProgressExtracting
	p.observe(event)
	event.Stage = image.ProgressLayerComplete
	p.observe(event)
	p.observe(image.Progress{Stage: image.ProgressCached, Reference: "redis:8"})
	text := output.String()
	if strings.ContainsAny(text, "\x1b\r") || strings.Count(text, "Downloading") != 2 || !strings.Contains(text, "50%") || !strings.Contains(text, "Pull complete") || !strings.Contains(text, "Using cached image redis:8") {
		t.Fatalf("plain progress: %q", text)
	}
	unknown := layerProgressLine(image.Progress{Stage: image.ProgressDownloading, Layer: "layer", Current: 123}, false)
	if strings.Contains(unknown, "%") || !strings.Contains(unknown, "123B") {
		t.Fatalf("invented unknown-size percentage: %q", unknown)
	}
}

func TestTTYProgressRewritesBoundedFrameAndFinishesOnFailure(t *testing.T) {
	var output bytes.Buffer
	p := newPullProgress(&output, "tty")
	for i := 1; i <= 20; i++ {
		event := image.Progress{Stage: image.ProgressDownloading, Layer: strings.Repeat("b", 64), Index: i, Current: 5, Total: 10}
		p.observe(event)
		event.Stage = image.ProgressExtracting
		p.observe(event)
		event.Stage = image.ProgressLayerComplete
		p.observe(event)
	}
	p.observe(image.Progress{Stage: image.ProgressFailed, Layer: strings.Repeat("b", 64), Index: 21})
	if len(p.rows) != 6 || p.lines != 6 || !strings.Contains(output.String(), "\x1b[6A") || !strings.HasSuffix(output.String(), "bbbbbbbbbbbb: Failed\n") {
		t.Fatalf("TTY rows or failure: %+v %q", p.rows, output.String())
	}
	if strings.Contains(output.String(), "Image ready") {
		t.Fatal("failed operation reported success")
	}
	p.observe(image.Progress{Stage: image.ProgressReady, Reference: "test:latest"})
	if p.lines != 0 {
		t.Fatal("image completion left an active frame")
	}
}

type failingProgressWriter struct{ calls int }

func (w *failingProgressWriter) Write([]byte) (int, error) {
	w.calls++
	return 0, errors.New("closed output")
}

func TestProgressStopsWritingAfterOutputFailure(t *testing.T) {
	writer := &failingProgressWriter{}
	p := newPullProgress(writer, "plain")
	for range 100 {
		p.observe(image.Progress{Stage: image.ProgressReady, Reference: "test:latest"})
	}
	if writer.calls != 1 {
		t.Fatalf("failed writer retried %d times", writer.calls)
	}
}
