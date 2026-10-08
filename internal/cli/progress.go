package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/mingo-liu/casklet/internal/image"
)

func validateProgressMode(mode string) error {
	switch mode {
	case "auto", "plain", "tty":
		return nil
	}
	return errors.New("--progress must be auto, plain, or tty")
}

func progressMode(mode string, output io.Writer) string {
	if mode != "" && mode != "auto" {
		return mode
	}
	if file, ok := output.(*os.File); ok && os.Getenv("TERM") != "dumb" && isProgressTerminal(file) {
		return "tty"
	}
	return "plain"
}

// Resolve auto on the Mac before SSH removes terminal properties. Appending the
// option after existing flags preserves explicit mode choices and workload args.
func guestProgressArguments(args []string, r Request, stderr io.Writer) []string {
	if r.Action != "image-pull" && (r.Action != "run" || r.Config.Image == "") {
		return args
	}
	index := len(args)
	for i, arg := range args {
		if arg == "--" {
			index = i
			break
		}
	}
	if r.Action == "image-pull" && index == len(args) {
		index-- // The sole positional reference follows the flags.
	}
	result := append([]string(nil), args[:index]...)
	result = append(result, "--progress="+progressMode(r.Progress, stderr))
	return append(result, args[index:]...)
}

type pullProgress struct {
	output io.Writer
	tty    bool
	rows   []image.Progress
	lines  int
	last   map[int]image.Progress
	times  map[int]time.Time
	failed bool
	now    func() time.Time
}

func newPullProgress(output io.Writer, mode string) *pullProgress {
	return &pullProgress{output: output, tty: progressMode(mode, output) == "tty", last: make(map[int]image.Progress), times: make(map[int]time.Time), now: time.Now}
}

func (p *pullProgress) write(text string) {
	if !p.failed {
		written, err := io.WriteString(p.output, text)
		p.failed = err != nil || written != len(text)
	}
}

func (p *pullProgress) observe(event image.Progress) {
	if p.failed {
		return
	}
	if event.Index == 0 {
		// Leave the completed layer frame above the next image-level message.
		p.lines = 0
		p.rows = nil
		var message string
		switch event.Stage {
		case image.ProgressResolving:
			message = fmt.Sprintf("Pulling %s (linux/%s)", event.Reference, runtime.GOARCH)
		case image.ProgressWaiting:
			message = "Waiting for image store lock"
		case image.ProgressVerifyingImage:
			message = "Verifying image"
		case image.ProgressPublishing:
			message = "Saving image"
		case image.ProgressCached:
			message = "Using cached image " + event.Reference
		case image.ProgressUpToDate:
			message = "Image is up to date for " + event.Reference
		case image.ProgressReady:
			message = "Image ready: " + event.Reference
		}
		if message != "" {
			p.write(message + "\n")
		}
		return
	}
	previous, exists := p.last[event.Index]
	now := p.now()
	if exists && event.Stage == previous.Stage && now.Sub(p.times[event.Index]) < time.Second && !p.tty {
		return
	}
	p.last[event.Index], p.times[event.Index] = event, now
	if !p.tty {
		p.write(layerProgressLine(event, false) + "\n")
		return
	}
	found := false
	for i := range p.rows {
		if p.rows[i].Index == event.Index {
			p.rows[i] = event
			found = true
			break
		}
	}
	if !found {
		p.rows = append(p.rows, event)
		if len(p.rows) > 6 {
			p.rows = p.rows[len(p.rows)-6:]
		}
	}
	var frame strings.Builder
	if p.lines > 0 {
		fmt.Fprintf(&frame, "\x1b[%dA", p.lines)
	}
	for _, row := range p.rows {
		frame.WriteString("\r\x1b[2K")
		frame.WriteString(layerProgressLine(row, true))
		frame.WriteByte('\n')
	}
	p.write(frame.String())
	p.lines = len(p.rows)
}

func layerProgressLine(event image.Progress, bar bool) string {
	id := strings.TrimPrefix(event.Layer, "sha256:")
	if len(id) > 12 {
		id = id[:12]
	}
	status := map[image.ProgressStage]string{
		image.ProgressDownloading: "Downloading", image.ProgressDownloaded: "Download complete",
		image.ProgressVerifying: "Verifying", image.ProgressExtracting: "Extracting",
		image.ProgressLayerComplete: "Pull complete", image.ProgressFailed: "Failed", image.ProgressCanceled: "Canceled",
	}[event.Stage]
	line := fmt.Sprintf("%s: %s", id, status)
	if event.Stage != image.ProgressDownloading && event.Stage != image.ProgressExtracting {
		return line
	}
	if event.Total <= 0 {
		return line + " " + progressBytes(event.Current)
	}
	percent := min(float64(event.Current)/float64(event.Total)*100, 100)
	if bar {
		filled := int(percent / 100 * 16)
		line += " [" + strings.Repeat("=", filled) + strings.Repeat(" ", 16-filled) + "]"
	}
	return fmt.Sprintf("%s %s/%s (%3.0f%%)", line, progressBytes(event.Current), progressBytes(event.Total), percent)
}

func progressBytes(value int64) string {
	if value < 1024 {
		return fmt.Sprintf("%dB", value)
	}
	size := float64(value)
	for _, unit := range []string{"KiB", "MiB", "GiB", "TiB"} {
		size /= 1024
		if size < 1024 || unit == "TiB" {
			return fmt.Sprintf("%.1f%s", size, unit)
		}
	}
	return ""
}
