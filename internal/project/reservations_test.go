package project

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mingo-liu/casklet/internal/config"
	"github.com/mingo-liu/casklet/internal/container"
)

func TestPreviewBatchesReservationQueries(t *testing.T) {
	fake, runner, plan := deployedProject(t)
	for i := 0; i < 126; i++ {
		fake.records = append(fake.records, container.Record{ID: fmt.Sprintf("%032x", 100+i), Name: fmt.Sprintf("other-%d", i), State: container.StateExited, Generation: 1})
	}
	inspections, batches := 0, 0
	runner.Call = func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] == "inspect" {
			inspections++
		}
		if args[0] == "__project-reservations" {
			batches++
		}
		return fake.call(ctx, args...)
	}
	if _, err := runner.Up(context.Background(), plan, UpOptions{DryRun: true, Timeout: time.Second}); err != nil {
		t.Fatal(err)
	}
	if inspections != 0 || batches != 2 || len(fake.mutations) != 0 {
		t.Fatalf("inspect=%d batches=%d mutations=%v", inspections, batches, fake.mutations)
	}
}

func TestReservationPagesRejectChangedMissingAndMalformedRecords(t *testing.T) {
	record := container.Record{ID: strings.Repeat("1", 32), Name: "other", State: container.StateRunning, Generation: 3}
	valid := Reservation{ID: record.ID, Name: record.Name, State: record.State, Generation: record.Generation}
	for _, scenario := range []string{"missing", "duplicate", "generation", "name", "identity", "state", "ports", "cursor", "oversized", "selected", "json"} {
		t.Run(scenario, func(t *testing.T) {
			page := ReservationPage{Records: []Reservation{valid}}
			removed := map[string]bool{}
			all := []container.Record{record}
			switch scenario {
			case "missing":
				page.Records = []Reservation{}
			case "duplicate":
				page.Records = append(page.Records, valid)
			case "generation":
				page.Records[0].Generation++
			case "name":
				page.Records[0].Name = "replacement"
			case "identity":
				page.Records[0].ID = "bad"
			case "state":
				page.Records[0].State = "bad"
			case "ports":
				page.Records[0].Publish = []config.PortMapping{{HostIP: "127.0.0.1"}}
			case "cursor":
				page.Next = valid.ID
			case "oversized":
				page.Records = make([]Reservation, ReservationPageSize+1)
			case "selected":
				removed[record.ID] = true
				all = append(all, container.Record{ID: strings.Repeat("2", 32)})
			}
			runner := Runner{Call: func(context.Context, ...string) ([]byte, error) {
				if scenario == "json" {
					return []byte("{"), nil
				}
				return json.Marshal(page)
			}}
			if _, err := runner.reservations(context.Background(), all, removed); err == nil {
				t.Fatal("invalid reservation response accepted")
			}
		})
	}
}

func TestReservationPagesHonorCancellationAndSkipSelected(t *testing.T) {
	fake, runner, plan := deployedProject(t)
	selected := fake.records[1].ID
	queries := 0
	runner.Call = func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] == "__project-reservations" {
			queries++
			if len(args) != 3 || args[2] != selected {
				t.Fatalf("selected record not excluded: %v", args)
			}
		}
		return fake.call(ctx, args...)
	}
	if _, err := runner.Up(context.Background(), plan, UpOptions{DryRun: true, Recreate: []string{"api"}}); err != nil || queries != 1 {
		t.Fatalf("exclusion: %v queries=%d", err, queries)
	}
	ctx, cancel := context.WithCancel(context.Background())
	count := 0
	runner.Call = func(context.Context, ...string) ([]byte, error) {
		count++
		cancel()
		page := ReservationPage{Records: make([]Reservation, ReservationPageSize)}
		for i := range page.Records {
			page.Records[i] = Reservation{ID: fmt.Sprintf("%032x", i+1), Name: fmt.Sprintf("other-%d", i), State: container.StateExited}
		}
		page.Next = page.Records[len(page.Records)-1].ID
		return json.Marshal(page)
	}
	if _, err := runner.reservations(ctx, []container.Record{{ID: fmt.Sprintf("%032x", 1), Name: "other-0", State: container.StateExited}}, nil); !errors.Is(err, context.Canceled) || count != 1 {
		t.Fatalf("cancellation: %v calls=%d", err, count)
	}
	runner.Call = func(context.Context, ...string) ([]byte, error) {
		t.Fatal("queried reservations when all records were selected")
		return nil, nil
	}
	if _, err := runner.reservations(context.Background(), []container.Record{{ID: selected}}, map[string]bool{selected: true}); err != nil {
		t.Fatal(err)
	}
}
