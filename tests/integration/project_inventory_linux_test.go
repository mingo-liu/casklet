//go:build linux

package integration

import (
	"context"
	encodingbinary "encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mingo-liu/casklet/internal/config"
	"github.com/mingo-liu/casklet/internal/container"
	"golang.org/x/sys/unix"
)

func TestProjectInventorySurvivesLargeUnrelatedHistory(t *testing.T) {
	require(t)
	t.Setenv("CASKLET_MACHINE_STATE_DIR", t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	store, err := container.OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("inventory-%x", time.Now().UnixNano())
	var ids []string
	t.Cleanup(func() {
		backgroundCLI(t, "project", "down", name)
		for _, id := range ids {
			if err := store.Remove(context.Background(), id); err != nil {
				t.Error(err)
			}
		}
	})
	cfg := config.Config{RootFS: template, Hostname: "history", Memory: 8 << 20, PidsLimit: 8,
		Command: []string{"/bin/sh", "-c", strings.Repeat(" ", 32<<10) + "true"}}
	for i := 0; i < 256; i++ {
		record, err := store.Create(ctx, cfg, fmt.Sprintf("%s-history-%d", name, i))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, record.ID)
		if err := store.Update(ctx, record.ID, func(record *container.Record) error {
			record.State, record.ExitReason = container.StateExited, "exited"
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if output := backgroundSuccess(t, "ps", "-a", "--json"); len(output) <= 8<<20 {
		t.Fatalf("fixture did not exceed the old response limit: %d", len(output))
	}
	t.Run("inventory state reads", func(t *testing.T) {
		assertProjectPageReads(t, ids, []string{"__project-records", name, "inventory"}, 256)
	})
	t.Run("reservation state reads", func(t *testing.T) {
		// Each selected record is read once for lifecycle state and once by
		// the generation-fenced configuration snapshot.
		assertProjectPageReads(t, ids, []string{"__project-reservations", "-"}, 512)
	})
	file := filepath.Join(t.TempDir(), "project.json")
	document := map[string]any{"name": name, "networks": []string{}, "services": map[string]any{
		"app": map[string]any{"run": map[string]any{"rootfs": template, "network": "none", "command": []string{"/bin/sh", "-c", "echo inventory-ready; sleep 300"}}},
	}}
	data, _ := json.Marshal(document)
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	backgroundSuccess(t, "project", "up", "--config", file, "--wait", "--timeout", "20s")
	output := backgroundSuccess(t, "project", "ps", "--json", name)
	var records []container.Record
	if json.Unmarshal([]byte(output), &records) != nil || len(records) != 1 || len(records[0].Command) == 0 {
		t.Fatalf("project listing lost member data: %s", output)
	}
	if output := backgroundSuccess(t, "project", "logs", name); !strings.Contains(output, "[app] inventory-ready") {
		t.Fatalf("project logs: %s", output)
	}
	backgroundSuccess(t, "project", "up", "--config", file, "--dry-run")
	backgroundSuccess(t, "project", "down", name)
	if output := backgroundSuccess(t, "project", "ps", "--json", name); output != "[]\n" {
		t.Fatalf("project removal left records: %s", output)
	}
	for _, id := range ids {
		if _, err := store.Get(ctx, id); err != nil {
			t.Fatalf("unrelated history changed: %v", err)
		}
	}
}

func assertProjectPageReads(t *testing.T, ids, firstArgs []string, want int) {
	t.Helper()
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { unix.Close(fd) })
	for _, id := range ids {
		if _, err := unix.InotifyAddWatch(fd, filepath.Join("/var/lib/casklet/containers", id, "state.json"), unix.IN_OPEN|unix.IN_CLOSE_NOWRITE); err != nil {
			t.Fatal(err)
		}
	}
	args := append([]string(nil), firstArgs...)
	reads, pages := 0, 0
	for {
		var page struct {
			Records []json.RawMessage `json:"records"`
			Next    string            `json:"next"`
		}
		output := backgroundSuccess(t, args...)
		if err := json.Unmarshal([]byte(output), &page); err != nil {
			t.Fatal(err)
		}
		pages++
		buffer := make([]byte, 64<<10)
		for {
			n, err := unix.Read(fd, buffer)
			if errors.Is(err, unix.EAGAIN) {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			for offset := 0; offset < n; {
				mask := encodingbinary.NativeEndian.Uint32(buffer[offset+4:])
				if mask&unix.IN_Q_OVERFLOW != 0 {
					t.Fatal("state read watch overflowed")
				}
				if mask&unix.IN_OPEN != 0 {
					reads++
				}
				offset += unix.SizeofInotifyEvent + int(encodingbinary.NativeEndian.Uint32(buffer[offset+12:]))
			}
		}
		if page.Next == "" {
			break
		}
		if pages > len(ids) || len(page.Records) != 64 {
			t.Fatal("invalid page continuation")
		}
		args = append(append([]string(nil), firstArgs[:len(firstArgs)-1]...), page.Next)
		if firstArgs[0] == "__project-records" {
			args = append(append([]string(nil), firstArgs...), page.Next)
		}
	}
	if reads != want {
		t.Fatalf("%d history records across %d pages: state opens=%d want=%d", len(ids), pages, reads, want)
	}
	t.Logf("%d history records across %d pages: %d state opens", len(ids), pages, reads)
}
