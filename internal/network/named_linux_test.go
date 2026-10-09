//go:build linux

package network

import (
	"context"
	"encoding/json"
	"errors"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNamedRoutesAndCanonicalMetadata(t *testing.T) {
	for _, test := range []struct {
		routes   string
		conflict bool
	}{{`[{"dst":"default"}]`, false}, {`[{"dst":"10.232.0.0/24"}]`, true}, {`[{"dst":"10.232.0.9"}]`, true}, {`[{"dst":"10.0.0.0/8"}]`, true}, {`[{"dst":"10.232.1.0/24"}]`, false}} {
		err := checkNamedRoutes([]byte(test.routes), "10.232.0.0/24")
		if (err != nil) != test.conflict {
			t.Fatalf("%s conflict=%t err=%v", test.routes, test.conflict, err)
		}
	}
	if os.Geteuid() != 0 {
		t.Skip("metadata ownership regression requires root")
	}
	s := &Store{root: t.TempDir()}
	r := Record{"demo", time.Now().UTC(), namedIdentity("demo"), "10.232.0.0/24", "10.232.0.1"}
	if err := writeJSON(filepath.Join(s.root, "demo.json"), r); err != nil {
		t.Fatal(err)
	}
	if _, err := s.read("demo"); err != nil {
		t.Fatal(err)
	}
	r.Subnet = "10.232.0.2/24"
	r.Gateway = "10.232.0.3"
	writeJSON(filepath.Join(s.root, "demo.json"), r)
	if _, err := s.read("demo"); err == nil {
		t.Fatal("noncanonical subnet accepted")
	}
}
func TestNamedLeaseReferenceAndCancelledLock(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("network leases require root")
	}
	s := &Store{root: t.TempDir()}
	r := Record{"demo", time.Now().UTC(), namedIdentity("demo"), "10.232.0.0/24", "10.232.0.1"}
	writeJSON(filepath.Join(s.root, "demo.json"), r)
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
	json.Unmarshal(data, &fields)
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
	os.WriteFile(outside, []byte("keep"), 0600)
	if err := os.Symlink(outside, temp); err != nil {
		t.Fatal(err)
	}
	if _, err := s.List(context.Background()); err == nil {
		t.Fatal("unsafe temp symlink accepted")
	}
	if b, err := os.ReadFile(outside); err != nil || string(b) != "keep" {
		t.Fatal("unsafe recovery touched outside file")
	}
	os.Remove(temp)
	os.Symlink(outside, filepath.Join(s.root, "demo.json"))
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
	unix.Flock(int(f.Fd()), unix.LOCK_UN)
	if live, err := activeRun(path); err != nil || live {
		t.Fatalf("released lock visible: %t %v", live, err)
	}
}
