package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/mingo-liu/casklet/internal/config"
	"github.com/mingo-liu/casklet/internal/container"
	"github.com/mingo-liu/casklet/internal/project"
)

func TestProjectReservationsBoundSnapshotsAndExcludePrivateData(t *testing.T) {
	records := make([]container.Record, 129)
	byID := map[string]container.Record{}
	for i := range records {
		records[i] = container.Record{ID: fmt.Sprintf("%032x", i+1), Name: fmt.Sprintf("record-%d", i), State: container.StateRunning, Generation: uint64(i)}
		byID[records[i].ID] = records[i]
	}
	excluded := []string{records[0].ID, records[128].ID}
	calls := 0
	snapshot := func(ctx context.Context, id string) (container.Record, config.Config, error) {
		calls++
		if id == excluded[0] || id == excluded[1] {
			t.Fatal("opened an excluded replacement's configuration")
		}
		return byID[id], config.Config{Network: "demo", NetworkAliases: []string{"api"}, Publish: []config.PortMapping{{HostIP: "127.0.0.1", HostPort: 39123, ContainerPort: 80, Protocol: "tcp"}},
			Command: []string{strings.Repeat("private-command", 1<<16)}, Env: []string{"TOKEN=private-secret"}, RootFS: "/private-source"}, nil
	}
	cursor := ""
	for i, expected := range []int{64, 63} {
		var out bytes.Buffer
		before := calls
		if err := writeProjectReservations(context.Background(), &out, testRecordPage(records, cursor, "", true, excluded), snapshot); err != nil {
			t.Fatal(err)
		}
		if calls-before != expected || out.Len() > 64<<10 || strings.Contains(out.String(), "private") || strings.Contains(out.String(), "TOKEN") {
			t.Fatalf("unbounded/private page: calls=%d bytes=%d", calls-before, out.Len())
		}
		var page project.ReservationPage
		if err := json.Unmarshal(out.Bytes(), &page); err != nil || len(page.Records) != expected || page.Records[0].Publish[0].HostPort != 39123 {
			t.Fatalf("invalid page: %+v %v", page, err)
		}
		if (page.Next == "") != (i == 1) {
			t.Fatalf("incorrect continuation: %q", page.Next)
		}
		cursor = page.Next
	}
}

func TestProjectReservationsRejectSnapshotChangesAndInvalidSelections(t *testing.T) {
	record := container.Record{ID: strings.Repeat("1", 32), Generation: 1}
	for _, scenario := range []string{"generation", "identity", "removed", "cancel"} {
		ctx, cancel := context.WithCancel(context.Background())
		snapshot := func(context.Context, string) (container.Record, config.Config, error) {
			current := record
			switch scenario {
			case "generation":
				current.Generation++
			case "identity":
				current.ID = strings.Repeat("2", 32)
			case "removed":
				return current, config.Config{}, container.ErrNotFound
			case "cancel":
				t.Fatal("snapshot called after cancellation")
			}
			return current, config.Config{}, nil
		}
		if scenario == "cancel" {
			cancel()
		}
		var out bytes.Buffer
		if err := writeProjectReservations(ctx, &out, container.RecordPage{Records: []container.Record{record}}, snapshot); err == nil || out.Len() != 0 {
			t.Fatalf("scenario=%s output=%s err=%v", scenario, out.String(), err)
		}
		cancel()
	}
	for _, args := range [][]string{{"__project-reservations"}, {"__project-reservations", "bad"}, {"__project-reservations", "-", "bad"}, {"__project-reservations", "-", record.ID, record.ID}} {
		if _, err := Parse(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	if r, err := Parse([]string{"__project-reservations", "-", record.ID}); err != nil || r.Cursor != "" || len(r.References) != 1 {
		t.Fatalf("valid selection: %+v %v", r, err)
	}
	if err := writeProjectReservations(context.Background(), failingWriter{}, container.RecordPage{}, nil); err == nil {
		t.Fatal("output error lost")
	}
}
