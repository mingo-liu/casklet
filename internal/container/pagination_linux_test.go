//go:build linux

package container

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mingo-liu/casklet/internal/config"
	"golang.org/x/sys/unix"
)

func paginationStore(t *testing.T, count int) (*Store, []Record) {
	t.Helper()
	store := testStore(t)
	records := make([]Record, count)
	for i := range records {
		record := Record{Version: 1, ID: fmt.Sprintf("%032x", i+1), Name: fmt.Sprintf("page-%d", i), State: StateExited,
			CreatedAt: time.Unix(int64(count-i), 0), Command: []string{"/bin/sh", "-c", strings.Repeat(" ", 32<<10) + "true"}}
		dir := filepath.Join(store.root, record.ID)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := store.writeJSON(dir, "state.json", record, maxRecordBytes); err != nil {
			t.Fatal(err)
		}
		cfg := testConfig()
		cfg.Network = "bridge"
		cfg.Publish = []config.PortMapping{{HostIP: "127.0.0.1", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}}
		if err := store.writeJSON(dir, "config.json", cfg, maxConfigBytes); err != nil {
			t.Fatal(err)
		}
		records[i] = record
	}
	return store, records
}

// Watch real state-file opens rather than a mock reader. Include close events
// so repeated opens cannot be coalesced into a single inotify event.
func watchRecordReads(t *testing.T, store *Store, records []Record) func() int {
	t.Helper()
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { unix.Close(fd) })
	for _, record := range records {
		if _, err := unix.InotifyAddWatch(fd, filepath.Join(store.root, record.ID, "state.json"), unix.IN_OPEN|unix.IN_CLOSE_NOWRITE); err != nil {
			t.Fatal(err)
		}
	}
	return func() int {
		count := 0
		buffer := make([]byte, 64<<10)
		for {
			n, err := unix.Read(fd, buffer)
			if errors.Is(err, unix.EAGAIN) {
				return count
			}
			if err != nil {
				t.Fatal(err)
			}
			for offset := 0; offset < n; {
				mask := binary.NativeEndian.Uint32(buffer[offset+4:])
				if mask&unix.IN_Q_OVERFLOW != 0 {
					t.Fatal("record read watch overflowed")
				}
				if mask&unix.IN_OPEN != 0 {
					count++
				}
				offset += unix.SizeofInotifyEvent + int(binary.NativeEndian.Uint32(buffer[offset+12:]))
			}
		}
	}
}

func TestRecordPagesReadEachStateOnce(t *testing.T) {
	store, records := paginationStore(t, 256)
	reads := watchRecordReads(t, store, records)
	ctx := context.Background()
	legacyReads := 0
	for i := 0; i < 4; i++ {
		if _, err := store.List(ctx); err != nil {
			t.Fatal(err)
		}
		legacyReads += reads()
	}
	options := RecordPageOptions{}
	total, pages, pageReads := 0, 0, 0
	for {
		page, err := store.ListPage(ctx, options)
		if err != nil {
			t.Fatal(err)
		}
		pages++
		pageReads += reads()
		for _, record := range page.Records {
			if record.ID != records[total].ID || !record.CreatedAt.Equal(records[total].CreatedAt) {
				t.Fatal("page lost ID ordering or complete member data")
			}
			total++
		}
		if len(page.Records) != RecordPageSize {
			t.Fatalf("short page: %d", len(page.Records))
		}
		if page.Next == "" {
			break
		}
		if page.Next != page.Records[len(page.Records)-1].ID {
			t.Fatal("continuation is not the last returned ID")
		}
		options.After = page.Next
	}
	if pages != 4 || total != len(records) || pageReads != 256 || legacyReads != 1024 {
		t.Fatalf("pages=%d records=%d state opens: legacy=%d paged=%d", pages, total, legacyReads, pageReads)
	}
	t.Logf("256 records across four pages: state opens %d -> %d", legacyReads, pageReads)
}

func TestRefreshedPagesReadEachStateOnceWithPorts(t *testing.T) {
	store, records := paginationStore(t, 2)
	reads := watchRecordReads(t, store, records)
	page, err := refreshedPage(context.Background(), store, RecordPageOptions{}, func(_ context.Context, record Record) (Record, error) {
		return record, nil
	})
	if err != nil || len(page.Records) != 2 {
		t.Fatalf("listing: %+v %v", page, err)
	}
	if got := reads(); got != 2 {
		t.Fatalf("state opens=%d, want 2", got)
	}
	if len(page.Records[0].Publish) != 1 || page.Records[0].Publish[0].HostPort != 8080 {
		t.Fatalf("listing lost ports: %+v", page)
	}
}

func TestRecordPagesSkipPriorAndExcludedStateFiles(t *testing.T) {
	store, records := paginationStore(t, 66)
	for _, index := range []int{0, 64} {
		if err := os.WriteFile(filepath.Join(store.root, records[index].ID, "state.json"), []byte("invalid JSON"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	page, err := store.ListPage(context.Background(), RecordPageOptions{After: records[0].ID, ExcludeIDs: []string{records[64].ID}})
	if err != nil || len(page.Records) != 64 || page.Next != "" || page.Records[63].ID != records[65].ID {
		t.Fatalf("cursor/exclusion read unrelated state: records=%d next=%q err=%v", len(page.Records), page.Next, err)
	}
	if _, err := store.ListPage(context.Background(), RecordPageOptions{}); err == nil {
		t.Fatal("unsafe state in the selected page was ignored")
	}
}

func TestRefreshedPagesFilterBeforeRefreshAndRefillRemovals(t *testing.T) {
	store, records := paginationStore(t, 130)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for i, record := range records {
		if i%2 == 0 {
			record.Labels = map[string]string{"project": "demo"}
			record.Health = &Health{Status: HealthHealthy}
			if err := store.writeJSON(filepath.Join(store.root, record.ID), "state.json", record, maxRecordBytes); err != nil {
				t.Fatal(err)
			}
		}
	}
	options := RecordPageOptions{Labels: map[string]string{"project": "demo"}}
	calls := 0
	page, err := refreshedPage(ctx, store, options, func(ctx context.Context, record Record) (Record, error) {
		calls++
		if record.Labels["project"] != "demo" {
			t.Fatal("refreshed an unrelated container")
		}
		// Updates require an exclusive metadata lock. Refresh must release its
		// listing lock first, preserving supervisor recovery's lock ordering.
		if err := store.Update(ctx, record.ID, func(*Record) error { return nil }); err != nil {
			return record, err
		}
		if record.ID == records[0].ID {
			return Record{}, ErrNotFound
		}
		return record, nil
	})
	if err != nil || calls != 65 || len(page.Records) != 64 || page.Next != records[128].ID || page.Records[0].ID != records[2].ID || page.Records[0].Health.Status != HealthStopped {
		t.Fatalf("refill: records=%d calls=%d next=%q err=%v", len(page.Records), calls, page.Next, err)
	}
	if len(page.Records[0].Publish) != 1 || page.Records[0].Publish[0].HostPort != 8080 {
		t.Fatalf("page lost saved port mappings: %+v", page.Records[0])
	}
	options.After = page.Next
	final, err := refreshedPage(ctx, store, options, func(context.Context, Record) (Record, error) {
		t.Fatal("refreshed an unrelated trailing record")
		return Record{}, nil
	})
	if err != nil || len(final.Records) != 0 || final.Next != "" {
		t.Fatalf("filtered final page: %+v %v", final, err)
	}
}

func TestRecordPagesValidateSelectionAndHonorCancellation(t *testing.T) {
	store, records := paginationStore(t, 1)
	for _, options := range []RecordPageOptions{
		{After: "../bad"}, {ExcludeIDs: []string{"bad"}}, {ExcludeIDs: make([]string, 65)}, {Labels: map[string]string{"bad key": "value"}},
	} {
		if _, err := store.ListPage(context.Background(), options); err == nil {
			t.Fatalf("accepted invalid selection: %+v", options)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reads := watchRecordReads(t, store, records)
	if _, err := store.ListPage(ctx, RecordPageOptions{}); !errors.Is(err, context.Canceled) || reads() != 0 {
		t.Fatalf("canceled page read state: %v", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	_, err := refreshedPage(ctx, store, RecordPageOptions{}, func(context.Context, Record) (Record, error) {
		cancel()
		return records[0], nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("refresh cancellation lost: %v", err)
	}
	cause := errors.New("refresh failed")
	if _, err := refreshedPage(context.Background(), store, RecordPageOptions{}, func(context.Context, Record) (Record, error) {
		return Record{}, cause
	}); !errors.Is(err, cause) {
		t.Fatalf("refresh error lost: %v", err)
	}
}
