//go:build linux

package container

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitialLogTailAcrossSegmentsAndEmptyFiles(t *testing.T) {
	tests := []struct {
		name     string
		segments []string // Oldest first.
		tail     int
		want     string
	}{
		{name: "zero", segments: []string{"old\n", "new\n"}, tail: 0, want: ""},
		{name: "all", segments: []string{"old\n", "new\n"}, tail: -1, want: "old\nnew\n"},
		{name: "terminated", segments: []string{"old\n", "new\n"}, tail: 1, want: "new\n"},
		{name: "unterminated", segments: []string{"old\n", "new"}, tail: 1, want: "new"},
		{name: "split line", segments: []string{"old\npart", "ial\nlast\n"}, tail: 2, want: "partial\nlast\n"},
		{name: "empty current", segments: []string{"old\nnew\n", "", ""}, tail: 1, want: "new\n"},
		{name: "empty lines", segments: []string{"old\n", "\n\n"}, tail: 2, want: "\n\n"},
		{name: "boundary", segments: []string{"old\n", "new\n"}, tail: 2, want: "old\nnew\n"},
		{name: "too many", segments: []string{"old\n", "new\n"}, tail: 100, want: "old\nnew\n"},
		{name: "empty", segments: []string{"", ""}, tail: 2, want: ""},
		{
			name:     "long line",
			segments: []string{"old\n" + strings.Repeat("x", 40<<10), "last"},
			tail:     1,
			want:     strings.Repeat("x", 40<<10) + "last",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, record, cfg, _ := rotationFixture(t, len(tt.segments))
			cfg.LogMaxSize = 128 << 10
			for i, data := range tt.segments {
				if err := os.WriteFile(filepath.Join(store.root, record.ID, logName(len(tt.segments)-i-1)), []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			data, cursor, err := store.logSnapshotTail(context.Background(), record.ID, cfg, logCursor{}, record.Generation, tt.tail)
			if cursor.pin != nil {
				defer func() {
					if cursor.pin != nil {
						_ = cursor.pin.Close()
					}
				}()
			}
			if err != nil || string(data) != tt.want {
				t.Fatalf("tail=%q want=%q err=%v", data, tt.want, err)
			}
			if got := snapshotLogs(t, store, record, cfg, &cursor); got != "" {
				t.Fatalf("repeated initial bytes: %q", got)
			}
		})
	}
}

func TestInitialLogTailSkipsPayloadAndFollowsRotation(t *testing.T) {
	for _, tail := range []int{0, 1} {
		store, record, cfg, _ := rotationFixture(t, 4)
		cfg.LogMaxSize = 4 << 20
		segment := bytes.Repeat([]byte("old entry\n"), (4<<20)/10)
		for i := 0; i < 4; i++ {
			if err := os.WriteFile(filepath.Join(store.root, record.ID, logName(i)), segment, 0600); err != nil {
				t.Fatal(err)
			}
		}
		data, cursor, err := store.logSnapshotTail(context.Background(), record.ID, cfg, logCursor{}, record.Generation, tail)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if cursor.pin != nil {
				_ = cursor.pin.Close()
			}
		}()
		want := ""
		if tail == 1 {
			want = "old entry\n"
		}
		if string(data) != want || cap(data) > 32<<10 {
			t.Fatalf("initial suffix size=%d capacity=%d", len(data), cap(data))
		}
		var (
			writer = &rotatingLog{store: store, id: record.ID, cfg: cfg}
			suffix = "next entry\n"
		) // Crosses the nearly full current segment.
		if _, err := writer.Write([]byte(suffix)); err != nil {
			t.Fatal(err)
		}
		if got := snapshotLogs(t, store, record, cfg, &cursor); got != suffix {
			t.Fatalf("follow=%q", got)
		}
	}
}

func TestInitialLogTailValidatesSkippedArtifacts(t *testing.T) {
	for _, tail := range []int{0, 1} {
		store, record, cfg, writer := rotationFixture(t, 3)
		if _, err := writer.Write([]byte("latest\n")); err != nil {
			t.Fatal(err)
		}
		outside := filepath.Join(t.TempDir(), "outside")
		if err := os.WriteFile(outside, []byte("private"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(store.root, record.ID, logName(2))); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.logSnapshotTail(context.Background(), record.ID, cfg, logCursor{}, record.Generation, tail); err == nil {
			t.Fatal("accepted unsafe skipped segment")
		}
	}
}

func TestInitialLogTailHonorsCancellationAndGeneration(t *testing.T) {
	store, record, cfg, writer := rotationFixture(t, 3)
	if _, err := writer.Write([]byte("one\ntwo\n")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := store.logSnapshotTail(ctx, record.ID, cfg, logCursor{}, record.Generation, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
	for _, tail := range []int{0, 1} {
		data, cursor, err := store.logSnapshotTail(context.Background(), record.ID, cfg, logCursor{}, record.Generation+1, tail)
		if err != nil || len(data) != 0 || cursor.pin != nil {
			t.Fatalf("crossed generation: %q %v", data, err)
		}
	}
}
