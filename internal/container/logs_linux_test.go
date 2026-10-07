//go:build linux

package container

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/mingo-liu/mini-docker/internal/config"
)

func rotationFixture(t *testing.T, files int) (*Store, Record, config.Config, *rotatingLog) {
	t.Helper()
	store := testStore(t)
	cfg := testConfig()
	cfg.LogMaxSize, cfg.LogMaxFiles = 1024, files
	record, err := store.Create(context.Background(), cfg, "logs")
	if err != nil {
		t.Fatal(err)
	}
	return store, record, cfg, &rotatingLog{store: store, id: record.ID, cfg: cfg}
}

func snapshotLogs(t *testing.T, store *Store, record Record, cfg config.Config, cursor *logCursor) string {
	t.Helper()
	data, next, err := store.logSnapshot(context.Background(), record.ID, cfg, *cursor, record.Generation)
	*cursor = next
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestLogRotationRetainsRecentBytesAcrossWritesAndStarts(t *testing.T) {
	for _, files := range []int{1, 3} {
		t.Run(logName(files), func(t *testing.T) {
			store, record, cfg, log := rotationFixture(t, files)
			input := strings.Repeat("a", 1024) + strings.Repeat("b", 1024) + strings.Repeat("c", 1024) + strings.Repeat("d", 1024) + "latest\n"
			// A new writer models a new supervisor; retention spans executions.
			if _, err := log.Write([]byte(input[:2000])); err != nil {
				t.Fatal(err)
			}
			log = &rotatingLog{store: store, id: record.ID, cfg: cfg}
			if _, err := log.Write([]byte(input[2000:])); err != nil {
				t.Fatal(err)
			}
			var cursor logCursor
			got := snapshotLogs(t, store, record, cfg, &cursor)
			defer cursor.pin.Close()
			want := input[(5-files)*1024:]
			if got != want || !log.truncated {
				t.Fatalf("retained %d bytes; want %d; discarded=%v", len(got), len(want), log.truncated)
			}
			entries, err := os.ReadDir(filepath.Join(store.root, record.ID))
			if err != nil {
				t.Fatal(err)
			}
			var total int64
			count := 0
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), "container.log") {
					stat, err := entry.Info()
					if err != nil {
						t.Fatal(err)
					}
					if stat.Size() > 1024 {
						t.Fatalf("oversized file: %s", entry.Name())
					}
					total += stat.Size()
					count++
				}
			}
			if count > files || total > int64(files)*1024 {
				t.Fatalf("unbounded retention: %d files, %d bytes", count, total)
			}
		})
	}
}

func TestLogFollowAcrossRotationAndRetentionOverrun(t *testing.T) {
	store, record, cfg, log := rotationFixture(t, 3)
	var cursor logCursor
	defer func() {
		if cursor.pin != nil {
			cursor.pin.Close()
		}
	}()
	write := func(data string) {
		t.Helper()
		if _, err := log.Write([]byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	first := strings.Repeat("a", 1000)
	write(first)
	if got := snapshotLogs(t, store, record, cfg, &cursor); got != first {
		t.Fatal("initial snapshot lost output")
	}
	next := strings.Repeat("b", 1200)
	write(next)
	if got := snapshotLogs(t, store, record, cfg, &cursor); got != next {
		t.Fatal("follow repeated or skipped rotated output")
	}
	if got := snapshotLogs(t, store, record, cfg, &cursor); got != "" {
		t.Fatal("follow repeated unchanged output")
	}
	write(strings.Repeat("c", 6*1024) + "final\n")
	var initial logCursor
	retained := snapshotLogs(t, store, record, cfg, &initial)
	defer initial.pin.Close()
	if got := snapshotLogs(t, store, record, cfg, &cursor); got != retained {
		t.Fatal("slow follow did not resume at retained output")
	}
	if got := snapshotLogs(t, store, record, cfg, &cursor); got != "" {
		t.Fatal("slow follow duplicated output")
	}
	// A reused container name or a new generation must not redirect the cursor.
	if err := store.Update(context.Background(), record.ID, func(r *Record) error { r.Generation++; return nil }); err != nil {
		t.Fatal(err)
	}
	write("next execution\n")
	if got := snapshotLogs(t, store, record, cfg, &cursor); got != "" {
		t.Fatal("follow crossed execution generations")
	}
}

func TestLogTailSpansRotatedFiles(t *testing.T) {
	store, record, cfg, log := rotationFixture(t, 3)
	data := strings.Repeat("x", 1020) + "\none\ntwo\nlast"
	if _, err := log.Write([]byte(data)); err != nil {
		t.Fatal(err)
	}
	var cursor logCursor
	snapshot := snapshotLogs(t, store, record, cfg, &cursor)
	defer cursor.pin.Close()
	var out bytes.Buffer
	if err := copyInitialLog(strings.NewReader(snapshot), int64(len(snapshot)), 3, &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "one\ntwo\nlast" {
		t.Fatalf("tail = %q", out.String())
	}
	if log.truncated {
		t.Fatal("rotation without eviction marked output discarded")
	}
}

func TestLogRotationRejectsUnsafeArtifactsWithoutMutating(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "public"} {
		t.Run(kind, func(t *testing.T) {
			store, record, cfg, log := rotationFixture(t, 3)
			outside := filepath.Join(t.TempDir(), "outside")
			if err := os.WriteFile(outside, []byte("preserved"), 0600); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(store.root, record.ID, logName(2))
			var err error
			switch kind {
			case "symlink":
				err = os.Symlink(outside, path)
			case "hardlink":
				err = os.Link(outside, path)
			case "public":
				err = os.WriteFile(path, []byte("preserved"), 0644)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := log.Write([]byte("blocked")); err == nil {
				t.Fatal("accepted unsafe rotation artifact")
			}
			data, err := os.ReadFile(outside)
			if err != nil || string(data) != "preserved" {
				t.Fatal("changed unrelated data")
			}
			var cursor logCursor
			if _, _, err := store.logSnapshot(context.Background(), record.ID, cfg, cursor, record.Generation); err == nil {
				t.Fatal("snapshot accepted unsafe artifact")
			}
			base, err := os.ReadFile(filepath.Join(store.root, record.ID, logName(0)))
			if err != nil || len(base) != 0 {
				t.Fatal("mutated log before validation")
			}
		})
	}
}

func TestLogRotationMigratesLegacyPrefixAndIgnoresTruncationFlag(t *testing.T) {
	store, record, cfg, log := rotationFixture(t, 3)
	old := strings.Repeat("old", 1000)
	if err := os.WriteFile(filepath.Join(store.root, record.ID, logName(0)), []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	log.truncated = true
	if _, err := log.Write([]byte("new output\n")); err != nil {
		t.Fatal(err)
	}
	var cursor logCursor
	got := snapshotLogs(t, store, record, cfg, &cursor)
	defer cursor.pin.Close()
	if got != old[len(old)-1024:]+"new output\n" {
		t.Fatal("legacy migration lost recent or new output")
	}
}

func TestLogSnapshotsAndRotationAreConcurrent(t *testing.T) {
	store, record, cfg, log := rotationFixture(t, 3)
	var group sync.WaitGroup
	group.Add(1)
	failure := make(chan error, 1)
	go func() {
		defer group.Done()
		for i := 0; i < 30; i++ {
			if _, err := log.Write(bytes.Repeat([]byte("x"), 500)); err != nil {
				failure <- err
				return
			}
		}
	}()
	var cursor logCursor
	defer func() {
		if cursor.pin != nil {
			cursor.pin.Close()
		}
	}()
	for i := 0; i < 30; i++ {
		data, next, err := store.logSnapshot(context.Background(), record.ID, cfg, cursor, record.Generation)
		cursor = next
		if err != nil {
			t.Fatal(err)
		}
		if len(data) > 3072 {
			t.Fatal("unbounded snapshot")
		}
	}
	group.Wait()
	select {
	case err := <-failure:
		t.Fatal(err)
	default:
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := store.logSnapshot(ctx, record.ID, cfg, cursor, record.Generation); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
}

func TestLogRotationRecoversMissingCurrentFileForSilentCommand(t *testing.T) {
	store, record, cfg, log := rotationFixture(t, 3)
	if _, err := log.Write([]byte("retained\n")); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(store.root, record.ID)
	// Simulate supervisor loss between rename and replacement creation.
	if err := os.Rename(filepath.Join(dir, logName(0)), filepath.Join(dir, logName(1))); err != nil {
		t.Fatal(err)
	}
	if err := log.Sync(); err != nil {
		t.Fatal(err)
	}
	var cursor logCursor
	if got := snapshotLogs(t, store, record, cfg, &cursor); got != "retained\n" {
		t.Fatalf("recovered logs = %q", got)
	}
	defer cursor.pin.Close()
	if stat, err := os.Stat(filepath.Join(dir, logName(0))); err != nil || stat.Size() != 0 {
		t.Fatalf("current file was not restored: %v", err)
	}
}
