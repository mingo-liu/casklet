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
	for _, tt := range []struct {
		name     string
		segments []string // Oldest first.
		tail     int
		want     string
	}{
		{"zero", []string{"old\n", "new\n"}, 0, ""},
		{"all", []string{"old\n", "new\n"}, -1, "old\nnew\n"},
		{"terminated", []string{"old\n", "new\n"}, 1, "new\n"},
		{"unterminated", []string{"old\n", "new"}, 1, "new"},
		{"split line", []string{"old\npart", "ial\nlast\n"}, 2, "partial\nlast\n"},
		{"empty current", []string{"old\nnew\n", "", ""}, 1, "new\n"},
		{"empty lines", []string{"old\n", "\n\n"}, 2, "\n\n"},
		{"boundary", []string{"old\n", "new\n"}, 2, "old\nnew\n"},
		{"too many", []string{"old\n", "new\n"}, 100, "old\nnew\n"},
		{"empty", []string{"", ""}, 2, ""},
		{"long line", []string{"old\n" + strings.Repeat("x", 40<<10), "last"}, 1, strings.Repeat("x", 40<<10) + "last"},
	} {
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
						cursor.pin.Close()
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
				cursor.pin.Close()
			}
		}()
		want := ""
		if tail == 1 {
			want = "old entry\n"
		}
		if string(data) != want || cap(data) > 32<<10 {
			t.Fatalf("initial suffix size=%d capacity=%d", len(data), cap(data))
		}
		writer := &rotatingLog{store: store, id: record.ID, cfg: cfg}
		suffix := "next entry\n" // Crosses the nearly full current segment.
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
