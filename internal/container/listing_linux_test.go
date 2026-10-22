//go:build linux

package container

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/mingo-liu/casklet/internal/config"
)

func TestListingReadsSavedPortsWithoutRecordMigration(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	cfg := testConfig()
	cfg.Network = "bridge"
	cfg.Publish = []config.PortMapping{
		{HostIP: "127.0.0.1", HostPort: 6379, ContainerPort: 6379, Protocol: "tcp"},
		{HostIP: "0.0.0.0", HostPort: 5353, ContainerPort: 53, Protocol: "udp"},
	}
	record, err := store.Create(ctx, cfg, "ports")
	if err != nil {
		t.Fatal(err)
	}
	reads := watchRecordReads(t, store, []Record{record})
	for _, state := range []string{StateRunning, StateExited} {
		record.State = state
		listed, err := listingRecord(ctx, store, record)
		if err != nil || !reflect.DeepEqual(listed.Publish, cfg.Publish) || listed.State != state || listed.ID != record.ID {
			t.Fatalf("listing %s: %+v %v", state, listed, err)
		}
	}
	if got := reads(); got != 0 {
		t.Fatalf("port lookup reopened lifecycle state %d times", got)
	}
	saved, err := store.Get(ctx, record.ID)
	if err != nil || len(saved.Publish) != 0 {
		t.Fatalf("listing rewrote durable record: %+v %v", saved, err)
	}
	missing := record
	missing.ID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := listingRecord(ctx, store, missing); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing container: %v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := listingRecord(canceled, store, record); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled listing: %v", err)
	}
}
