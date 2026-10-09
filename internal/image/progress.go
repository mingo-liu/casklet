package image

import (
	"context"
	"io"
	"sync"
	"time"
)

type ProgressStage string

const (
	ProgressResolving      ProgressStage = "resolving"
	ProgressWaiting        ProgressStage = "waiting"
	ProgressDownloading    ProgressStage = "downloading"
	ProgressDownloaded     ProgressStage = "downloaded"
	ProgressLayerCached    ProgressStage = "layer-cached"
	ProgressVerifying      ProgressStage = "verifying"
	ProgressExtracting     ProgressStage = "extracting"
	ProgressLayerComplete  ProgressStage = "layer-complete"
	ProgressVerifyingImage ProgressStage = "verifying-image"
	ProgressPublishing     ProgressStage = "publishing"
	ProgressCached         ProgressStage = "cached"
	ProgressUpToDate       ProgressStage = "up-to-date"
	ProgressReady          ProgressStage = "ready"
	ProgressFailed         ProgressStage = "failed"
	ProgressCanceled       ProgressStage = "canceled"
)

// Progress reports real stream bytes. Download totals are compressed manifest
// sizes; extraction counts both archive passes. Layer is the registry digest,
// while Index (one-based) distinguishes repeated layers in a manifest.
type Progress struct {
	Stage     ProgressStage
	Reference string
	Layer     string
	Index     int
	Layers    int
	Current   int64
	Total     int64
}

type progressKey struct{}

// WithProgress installs a synchronous observer for this operation only. The
// observer should return promptly; presentation belongs to the caller. Calls
// from concurrent layer workers serialize through the same observer mutex.
func WithProgress(ctx context.Context, observe func(Progress)) context.Context {
	var mutex sync.Mutex
	serialized := func(event Progress) {
		mutex.Lock()
		defer mutex.Unlock()
		if observe != nil {
			observe(event)
		}
	}
	return context.WithValue(ctx, progressKey{}, serialized)
}

func reportProgress(ctx context.Context, event Progress) {
	if observe, ok := ctx.Value(progressKey{}).(func(Progress)); ok && observe != nil {
		observe(event)
	}
}

// streamProgress bounds update frequency independently of stream chunk sizes.
// Completion and phase changes bypass throttling, including very small layers.
type streamProgress struct {
	ctx   context.Context
	event Progress
	last  time.Time
	now   func() time.Time
}

func (p *streamProgress) add(n int) {
	p.event.Current += int64(n)
	now := p.now()
	if p.last.IsZero() || now.Sub(p.last) >= 200*time.Millisecond {
		p.last = now
		reportProgress(p.ctx, p.event)
	}
}

func (p *streamProgress) finish(stage ProgressStage) {
	p.event.Stage = stage
	reportProgress(p.ctx, p.event)
}

type countingReader struct {
	reader io.Reader
	read   func(int)
}

func (r countingReader) Read(buf []byte) (int, error) {
	n, err := r.reader.Read(buf)
	if n > 0 {
		r.read(n)
	}
	return n, err
}
