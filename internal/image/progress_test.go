package image

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestStreamProgressCountsAndThrottlesWithoutLosingCompletion(t *testing.T) {
	var events []Progress
	ctx := WithProgress(context.Background(), func(event Progress) { events = append(events, event) })
	now := time.Unix(1, 0)
	p := streamProgress{ctx: ctx, event: Progress{Stage: ProgressDownloading, Index: 1, Total: 1000}, now: func() time.Time { return now }}
	p.add(10)
	for range 100 {
		p.add(1)
	}
	now = now.Add(200 * time.Millisecond)
	p.add(20)
	p.add(5)
	p.finish(ProgressDownloaded)
	var counts []int64
	for _, event := range events {
		counts = append(counts, event.Current)
	}
	if !reflect.DeepEqual(counts, []int64{10, 130, 135}) || events[2].Stage != ProgressDownloaded {
		t.Fatalf("throttled counts and final event: %+v", events)
	}
	// An observer is scoped to its operation, never inherited by other pulls.
	reportProgress(context.Background(), Progress{Stage: ProgressReady})
	if len(events) != 3 {
		t.Fatal("observer escaped its context")
	}
}

func TestProgressObserversSerializeParallelLayerWorkers(t *testing.T) {
	var events []Progress
	ctx := WithProgress(context.Background(), func(event Progress) { events = append(events, event) })
	var workers sync.WaitGroup
	for index := range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for range 100 {
				reportProgress(ctx, Progress{Stage: ProgressDownloading, Index: index + 1})
			}
		}()
	}
	workers.Wait()
	if len(events) != 800 {
		t.Fatalf("serialized observer lost events: %d", len(events))
	}
}
