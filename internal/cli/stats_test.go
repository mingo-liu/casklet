package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"testing"
	"time"

	"github.com/mingo-liu/casklet/internal/container"
)

func TestStatsSelectionsAndWatchParsing(t *testing.T) {
	for _, args := range [][]string{{"stats", "a", "b"}, {"stats", "--all"}, {"stats", "--watch", "--filter", "label=project=demo"}} {
		if _, err := Parse(args); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	for _, args := range [][]string{{"stats", "--watch"}, {"stats", "--all", "a"}, {"stats", "--filter", "label=x", "a"}, {"stats", "--watch", "--interval", "1ms", "a"}} {
		if _, err := Parse(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestStatsWatchPinsSelectionAndStreamsJSON(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inspections, samples := 0, 0
	var out bytes.Buffer
	ops := statsOperations{
		inspect: func(_ context.Context, ref string) (container.Inspection, error) {
			inspections++
			return container.Inspection{ID: "original", Name: ref}, nil
		},
		sample: func(_ context.Context, ids []string, _ time.Duration) ([]container.Statistics, error) {
			if !reflect.DeepEqual(ids, []string{"original"}) {
				t.Fatalf("selection redirected: %v", ids)
			}
			samples++
			if samples == 2 {
				cancel()
			}
			return []container.Statistics{{ID: ids[0], State: "removed"}}, nil
		},
	}
	err := executeStats(ctx, Request{References: []string{"worker", "worker"}, Watch: true, JSON: true, Interval: 10 * time.Millisecond}, &out, ops)
	if !errors.Is(err, context.Canceled) || inspections != 2 || samples != 2 {
		t.Fatalf("%v inspections=%d samples=%d", err, inspections, samples)
	}
	decoder := json.NewDecoder(&out)
	for i := 0; i < 2; i++ {
		var rows []container.Statistics
		if err := decoder.Decode(&rows); err != nil || len(rows) != 1 || rows[0].ID != "original" || rows[0].Name != "worker" {
			t.Fatalf("frame %d: %v %v", i, rows, err)
		}
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		t.Fatalf("extra output: %v", err)
	}
}

func TestStatsSelectorSortsFiltersAndEmptyWatchFinishes(t *testing.T) {
	calls := 0
	ops := statsOperations{
		list: func(context.Context, bool) ([]container.Record, error) {
			calls++
			return []container.Record{{ID: "b", Labels: map[string]string{"project": "demo"}}, {ID: "other"}, {ID: "a", Labels: map[string]string{"project": "demo"}}}, nil
		},
		sample: func(_ context.Context, ids []string, _ time.Duration) ([]container.Statistics, error) {
			if !reflect.DeepEqual(ids, []string{"a", "b"}) {
				t.Fatalf("ids %v", ids)
			}
			return []container.Statistics{{ID: "a"}, {ID: "b"}}, nil
		},
	}
	filter, _ := container.ParseFilter("label=project=demo")
	var out bytes.Buffer
	if err := executeStats(context.Background(), Request{Filters: []container.Filter{filter}, JSON: true}, &out, ops); err != nil || calls != 1 {
		t.Fatalf("%v calls=%d", err, calls)
	}
	ops.list = func(context.Context, bool) ([]container.Record, error) { return nil, nil }
	ops.sample = func(context.Context, []string, time.Duration) ([]container.Statistics, error) { return nil, nil }
	out.Reset()
	if err := executeStats(context.Background(), Request{All: true, Watch: true, JSON: true}, &out, ops); err != nil || out.String() != "[]\n" {
		t.Fatalf("empty %v %q", err, out.String())
	}
}

func TestStatsWatchWaitsWhenMetricsUnavailable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	calls := 0
	ops := statsOperations{
		inspect: func(context.Context, string) (container.Inspection, error) { return container.Inspection{ID: "a"}, nil },
		sample: func(context.Context, []string, time.Duration) ([]container.Statistics, error) {
			calls++
			return []container.Statistics{{ID: "a"}}, nil
		},
	}
	err := executeStats(ctx, Request{References: []string{"a"}, Watch: true, Interval: time.Second}, io.Discard, ops)
	if !errors.Is(err, context.DeadlineExceeded) || calls != 1 {
		t.Fatalf("busy watch: %v calls=%d", err, calls)
	}
}
