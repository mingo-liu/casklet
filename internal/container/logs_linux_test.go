//go:build linux

package container

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mingo-liu/mini-docker/internal/config"
)

type contentionLogWriter struct {
	*rotatingLog
	timedOut chan struct{}
}

func (writer contentionLogWriter) Write(data []byte) (int, error) {
	n, err := writer.rotatingLog.Write(data)
	if errors.Is(err, errLogBusy) {
		close(writer.timedOut)
	}
	return n, err
}

func TestLogCaptureResumesAfterLockTimeout(t *testing.T) {
	for _, metadata := range []bool{false, true} {
		name := "log"
		if metadata {
			name = "metadata"
		}
		t.Run(name, func(t *testing.T) { testLogCaptureResumesAfterLockTimeout(t, metadata) })
	}
}

func testLogCaptureResumesAfterLockTimeout(t *testing.T, metadata bool) {
	t.Helper()
	store, record, cfg, log := rotationFixture(t, 3)
	var lock *os.File
	var err error
	if metadata {
		lock, err = store.lock(context.Background(), false)
	} else {
		lock, _, err = store.lockLog(context.Background(), record.ID, false)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	timedOut := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- captureLog(reader, contentionLogWriter{log, timedOut}) }()
	if _, err := writer.Write([]byte("discarded during contention\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-timedOut:
	case <-time.After(7 * time.Second):
		t.Fatal("log lock wait was not bounded")
	}
	lock.Close()
	if _, err := writer.Write([]byte("saved after recovery\n")); err != nil {
		t.Fatal(err)
	}
	writer.Close()
	select {
	case err := <-done:
		if !errors.Is(err, errLogBusy) || !log.truncated {
			t.Fatalf("discard was not reported: %v, truncated=%v", err, log.truncated)
		}
	case <-time.After(7 * time.Second):
		t.Fatal("capture did not finish")
	}
	var cursor logCursor
	if got := snapshotLogs(t, store, record, cfg, &cursor); got != "saved after recovery\n" {
		t.Fatalf("capture did not resume: %q", got)
	}
	defer cursor.pin.Close()
}

func TestLogLockDoesNotBlockMetadataOrOtherContainers(t *testing.T) {
	store, record, _, _ := rotationFixture(t, 3)
	other, err := store.Create(context.Background(), testConfig(), "other")
	if err != nil {
		t.Fatal(err)
	}
	lock, _, err := store.lockLog(context.Background(), record.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := store.Update(ctx, record.ID, func(r *Record) error { r.State = StateExited; return nil }); err != nil {
		t.Fatalf("log lock blocked metadata: %v", err)
	}
	otherLog := &rotatingLog{store: store, id: other.ID, cfg: testConfig()}
	if _, err := otherLog.Write([]byte("independent\n")); err != nil {
		t.Fatal(err)
	}
	short, stop := context.WithTimeout(ctx, 20*time.Millisecond)
	defer stop()
	if err := store.Remove(short, record.ID); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("removal bypassed live log lock: %v", err)
	}
	lock.Close()
	if err := store.Remove(ctx, record.ID); err != nil {
		t.Fatal(err)
	}
}

func TestLogLockUpgradesOldRecordsAndRejectsUnsafeFiles(t *testing.T) {
	for _, kind := range []string{"missing", "symlink", "hardlink", "public"} {
		t.Run(kind, func(t *testing.T) {
			store, record, _, log := rotationFixture(t, 3)
			path := filepath.Join(store.root, record.ID, ".logs")
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(t.TempDir(), "outside")
			if err := os.WriteFile(outside, nil, 0600); err != nil {
				t.Fatal(err)
			}
			var err error
			switch kind {
			case "symlink":
				err = os.Symlink(outside, path)
			case "hardlink":
				err = os.Link(outside, path)
			case "public":
				err = os.WriteFile(path, nil, 0644)
			}
			if err != nil {
				t.Fatal(err)
			}
			_, err = log.Write([]byte("safe\n"))
			if (err == nil) != (kind == "missing") {
				t.Fatalf("lock %s: %v", kind, err)
			}
		})
	}
}

func TestLegacyRunningLogUsesMetadataLockUntilCompletion(t *testing.T) {
	store, record, _, _ := rotationFixture(t, 3)
	if err := store.Update(context.Background(), record.ID, func(r *Record) error {
		r.LogLocking, r.State = false, StateRunning
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	lock, _, err := store.lockLog(context.Background(), record.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := store.Update(ctx, record.ID, func(*Record) error { return nil }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("legacy snapshot did not fence the old writer: %v", err)
	}
	lock.Close()
	if err := store.Update(context.Background(), record.ID, func(r *Record) error { r.State = StateExited; return nil }); err != nil {
		t.Fatal(err)
	}
	lock, _, err = store.lockLog(context.Background(), record.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := store.Update(ctx, record.ID, func(*Record) error { return nil }); err != nil {
		t.Fatalf("completed legacy snapshot still blocked metadata: %v", err)
	}
	lock.Close()
	operation, err := store.AcquireOperation(ctx, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer operation.Close()
	next, err := store.BeginExecution(ctx, record.ID, record.Generation)
	if err != nil || !next.LogLocking {
		t.Fatalf("restarted legacy record did not enable log locking: %+v, %v", next, err)
	}
}

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

func TestLargeLogSnapshotPreservesSegmentsAndCursorSuffix(t *testing.T) {
	store := testStore(t)
	cfg := testConfig()
	cfg.LogMaxSize, cfg.LogMaxFiles = 128<<10, 3
	record, err := store.Create(context.Background(), cfg, "large-log")
	if err != nil {
		t.Fatal(err)
	}
	var expected bytes.Buffer
	for i := cfg.LogMaxFiles - 1; i >= 0; i-- {
		data := bytes.Repeat([]byte{byte('a' + i)}, (i+1)*35*1024)
		if err := os.WriteFile(filepath.Join(store.root, record.ID, logName(i)), data, 0600); err != nil {
			t.Fatal(err)
		}
		expected.Write(data)
	}
	var cursor logCursor
	defer func() {
		if cursor.pin != nil {
			cursor.pin.Close()
		}
	}()
	if got := snapshotLogs(t, store, record, cfg, &cursor); got != expected.String() {
		t.Fatal("large snapshot changed segment order or content")
	}
	if got := snapshotLogs(t, store, record, cfg, &cursor); got != "" {
		t.Fatal("unchanged large snapshot repeated output")
	}
	log := &rotatingLog{store: store, id: record.ID, cfg: cfg}
	suffix := strings.Repeat("suffix\n", 6000)
	if _, err := log.Write([]byte(suffix)); err != nil {
		t.Fatal(err)
	}
	if got := snapshotLogs(t, store, record, cfg, &cursor); got != suffix {
		t.Fatal("large snapshot cursor skipped or repeated appended bytes")
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
