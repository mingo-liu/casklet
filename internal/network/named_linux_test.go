//go:build linux

package network

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestNamedRoutes(t *testing.T) {
	tests := []struct {
		giveRoutes string
		wantErr    bool
	}{
		{giveRoutes: `[{"dst":"default"}]`, wantErr: false},
		{giveRoutes: `[{"dst":"10.232.0.0/24"}]`, wantErr: true},
		{giveRoutes: `[{"dst":"10.232.0.9"}]`, wantErr: true},
		{giveRoutes: `[{"dst":"10.0.0.0/8"}]`, wantErr: true},
		{giveRoutes: `[{"dst":"10.232.1.0/24"}]`, wantErr: false},
	}
	for _, tt := range tests {
		t.Run(tt.giveRoutes, func(t *testing.T) {
			err := checkNamedRoutes([]byte(tt.giveRoutes), "10.232.0.0/24")
			if (err != nil) != tt.wantErr {
				t.Fatalf("conflict = %t, want %t: %v", err != nil, tt.wantErr, err)
			}
		})
	}
}

func TestNamedCanonicalMetadata(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("metadata ownership regression requires root")
	}
	s := &Store{root: t.TempDir()}
	r := Record{
		Name:      "demo",
		CreatedAt: time.Now().UTC(),
		Bridge:    namedIdentity("demo"),
		Subnet:    "10.232.0.0/24",
		Gateway:   "10.232.0.1",
	}
	if err := writeJSON(filepath.Join(s.root, "demo.json"), r); err != nil {
		t.Fatal(err)
	}
	if _, err := s.read("demo"); err != nil {
		t.Fatal(err)
	}
	r.Subnet = "10.232.0.2/24"
	r.Gateway = "10.232.0.3"
	if err := writeJSON(filepath.Join(s.root, "demo.json"), r); err != nil {
		t.Fatal(err)
	}
	if _, err := s.read("demo"); err == nil {
		t.Fatal("noncanonical subnet accepted")
	}
}
func TestNamedLeaseReferenceAndCancelledLock(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("network leases require root")
	}
	s := &Store{root: t.TempDir()}
	r := Record{
		Name:      "demo",
		CreatedAt: time.Now().UTC(),
		Bridge:    namedIdentity("demo"),
		Subnet:    "10.232.0.0/24",
		Gateway:   "10.232.0.1",
	}
	if err := writeJSON(filepath.Join(s.root, "demo.json"), r); err != nil {
		t.Fatal(err)
	}
	lease, err := s.Acquire(context.Background(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Remove(context.Background(), "demo", func(context.Context, string) (bool, error) {
		t.Fatal("reference scan should follow exclusive lease")
		return false, nil
	}); !errors.Is(err, ErrInUse) {
		t.Fatalf("active removal: %v", err)
	}
	lease.Close()
	if err := s.Remove(context.Background(), "demo", func(context.Context, string) (bool, error) { return true, nil }); !errors.Is(err, ErrInUse) {
		t.Fatalf("retained reference removal: %v", err)
	}
	lock, err := s.lock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := s.List(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lock cancellation: %v", err)
	}
}
func TestNamedJournalPreservesLegacyAllocationSchema(t *testing.T) {
	record := allocation{BootID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", Address: "10.231.0.2"}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"network", "aliases"} {
		if _, ok := fields[key]; ok {
			t.Fatalf("legacy journal gained %q: %s", key, data)
		}
	}
}

func TestNamedMetadataRecoveryAndUnsafeArtifacts(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("network metadata ownership requires root")
	}
	s := &Store{root: t.TempDir()}
	temp := filepath.Join(s.root, ".network-state-interrupted")
	if err := os.WriteFile(temp, []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	if records, err := s.List(context.Background()); err != nil || len(records) != 0 {
		t.Fatalf("recover metadata: %v %+v", err, records)
	}
	if _, err := os.Stat(temp); !os.IsNotExist(err) {
		t.Fatal("interrupted save survived recovery")
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, temp); err != nil {
		t.Fatal(err)
	}
	if _, err := s.List(context.Background()); err == nil {
		t.Fatal("unsafe temp symlink accepted")
	}
	if b, err := os.ReadFile(outside); err != nil || string(b) != "keep" {
		t.Fatal("unsafe recovery touched outside file")
	}
	if err := os.Remove(temp); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(s.root, "demo.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Inspect(context.Background(), "demo"); err == nil {
		t.Fatal("metadata symlink accepted")
	}
}
func TestAbandonedRunDoesNotRemainInDNS(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("runtime lock ownership requires root")
	}
	path := t.TempDir()
	f, err := os.OpenFile(filepath.Join(path, "lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if live, err := activeRun(path); err != nil || live {
		t.Fatalf("abandoned lock visible: %t %v", live, err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if live, err := activeRun(path); err != nil || !live {
		t.Fatalf("live lock missing: %t %v", live, err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	if live, err := activeRun(path); err != nil || live {
		t.Fatalf("released lock visible: %t %v", live, err)
	}
}

func TestNamedProjectReceiptRejectsAdoptionAndReplacement(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("network ownership requires root")
	}
	store := &Store{root: t.TempDir()}
	record := Record{
		Name:      "demo",
		CreatedAt: time.Now().UTC(),
		Bridge:    namedIdentity("demo"),
		Subnet:    "10.232.0.0/24",
		Gateway:   "10.232.0.1",
	}
	if err := writeJSON(filepath.Join(store.root, "demo.json"), record); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateExclusive(context.Background(), "demo"); !errors.Is(err, os.ErrExist) {
		t.Fatalf("adopted existing network: %v", err)
	}
	check := func(context.Context, string) (bool, error) {
		t.Fatal("identity comparison must precede reference scanning")
		return false, nil
	}
	if err := store.RemoveIfCreated(context.Background(), "demo", record.CreatedAt.Add(time.Second), check); err == nil {
		t.Fatal("removed replacement identity")
	}
	if _, err := store.Inspect(context.Background(), "demo"); err != nil {
		t.Fatal(err)
	}
}
