//go:build linux

package network

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mingo-liu/casklet/internal/config"
	"golang.org/x/sys/unix"
)

type Store struct{ root string }

func privateDirectory(path string) error {
	var st unix.Stat_t
	if err := unix.Lstat(path, &st); err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR || st.Uid != 0 || st.Mode&0022 != 0 {
		return errors.New("network storage requires private root-owned directories")
	}
	return nil
}
func OpenStore() (*Store, error) {
	if os.Geteuid() != 0 {
		return nil, errors.New("named networks require root privileges inside Linux")
	}
	for _, p := range []string{"/var/lib/casklet", NamedRoot, runsRoot} {
		if err := os.Mkdir(p, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if err := privateDirectory(p); err != nil {
			return nil, err
		}
	}
	return &Store{NamedRoot}, nil
}
func namedLock(ctx context.Context, path string, how int) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != 0 || st.Mode&0077 != 0 || st.Nlink != 1 {
		f.Close()
		return nil, errors.New("unsafe named network lock")
	}
	for {
		if err := ctx.Err(); err != nil {
			f.Close()
			return nil, err
		}
		err := unix.Flock(fd, how|unix.LOCK_NB)
		if err == nil {
			return f, nil
		}
		if how == unix.LOCK_SH || !errors.Is(err, unix.EWOULDBLOCK) {
			f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}
func (s *Store) lock(ctx context.Context) (*os.File, error) {
	if err := privateDirectory(s.root); err != nil {
		return nil, err
	}
	l, err := namedLock(ctx, filepath.Join(s.root, ".lock"), unix.LOCK_EX)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(s.root)
	if err != nil {
		l.Close()
		return nil, err
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".network-state-") {
			continue
		}
		p := filepath.Join(s.root, entry.Name())
		var st unix.Stat_t
		if err := unix.Lstat(p, &st); err != nil {
			l.Close()
			return nil, err
		}
		if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != 0 || st.Mode&0077 != 0 || st.Nlink != 1 {
			l.Close()
			return nil, errors.New("unsafe interrupted network metadata")
		}
		if err := os.Remove(p); err != nil {
			l.Close()
			return nil, err
		}
	}
	return l, nil
}
func namedIdentity(name string) string {
	sum := sha256.Sum256([]byte(name))
	return "csn" + hex.EncodeToString(sum[:])[:10]
}
func (s *Store) read(name string) (Record, error) {
	var r Record
	if err := config.ValidateNetworkName(name); err != nil {
		return r, err
	}
	if err := readJSON(filepath.Join(s.root, name+".json"), &r); err != nil {
		return r, err
	}
	prefix, err := netip.ParsePrefix(r.Subnet)
	if err != nil || r.Name != name || r.CreatedAt.IsZero() || r.Bridge != namedIdentity(name) || prefix.Bits() != 24 || prefix.Masked() != prefix || !netip.MustParsePrefix("10.232.0.0/16").Contains(prefix.Addr()) || r.Gateway != prefix.Addr().Next().String() {
		return Record{}, errors.New("invalid named network metadata")
	}
	return r, nil
}
func (s *Store) list() ([]Record, error) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, err
	}
	out := []Record{}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if !strings.HasSuffix(e.Name(), ".json") {
			return nil, errors.New("unexpected named network metadata")
		}
		r, err := s.read(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}
func (s *Store) Create(ctx context.Context, name string) (Record, error) {
	if err := config.ValidateNetworkName(name); err != nil {
		return Record{}, err
	}
	l, err := s.lock(ctx)
	if err != nil {
		return Record{}, err
	}
	defer l.Close()
	if r, err := s.read(name); err == nil {
		return r, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return Record{}, err
	}
	records, err := s.list()
	if err != nil {
		return Record{}, err
	}
	used := map[string]bool{}
	for _, r := range records {
		used[r.Subnet] = true
		if r.Bridge == namedIdentity(name) {
			return Record{}, errors.New("named network bridge identity conflict")
		}
	}
	routes, err := command(ctx, "ip", "", "-j", "-4", "route", "show", "table", "all")
	if err != nil {
		return Record{}, err
	}
	for n := 0; n < 256; n++ {
		pool := fmt.Sprintf("10.232.%d.0/24", n)
		if used[pool] || checkNamedRoutes(routes, pool) != nil {
			continue
		}
		r := Record{name, time.Now().UTC(), namedIdentity(name), pool, fmt.Sprintf("10.232.%d.1", n)}
		return r, writeJSON(filepath.Join(s.root, name+".json"), r)
	}
	return Record{}, errors.New("named network subnet pool is exhausted or overlaps host routes")
}
func (s *Store) Inspect(ctx context.Context, name string) (Record, error) {
	l, err := s.lock(ctx)
	if err != nil {
		return Record{}, err
	}
	defer l.Close()
	return s.read(name)
}
func (s *Store) List(ctx context.Context) ([]Record, error) {
	l, err := s.lock(ctx)
	if err != nil {
		return nil, err
	}
	defer l.Close()
	return s.list()
}
func (s *Store) Acquire(ctx context.Context, name string) (*os.File, error) {
	l, err := s.lock(ctx)
	if err != nil {
		return nil, err
	}
	defer l.Close()
	if _, err := s.read(name); err != nil {
		return nil, err
	}
	return namedLock(ctx, filepath.Join(s.root, ".lease-"+name), unix.LOCK_SH)
}
func (s *Store) Remove(ctx context.Context, name string, referenced ReferenceCheck) error {
	l, err := s.lock(ctx)
	if err != nil {
		return err
	}
	defer l.Close()
	r, err := s.read(name)
	if err != nil {
		return err
	}
	lease, err := namedLock(ctx, filepath.Join(s.root, ".lease-"+name), unix.LOCK_SH)
	if err != nil {
		return err
	}
	defer lease.Close()
	if err := unix.Flock(int(lease.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return ErrInUse
	}
	if referenced == nil {
		return errors.New("network removal requires a retained-container reference check")
	}
	used, err := referenced(ctx, name)
	if err != nil {
		return err
	}
	if used {
		return ErrInUse
	}
	coordination, err := lock(ctx)
	if err != nil {
		return err
	}
	defer coordination.Close()
	records, err := allocations()
	if err != nil {
		return err
	}
	for _, a := range records {
		if a.Network == name {
			return ErrInUse
		}
	}
	if err := removeNamedBridge(ctx, r); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(s.root, name+".json")); err != nil {
		return err
	}
	dir, err := os.Open(s.root)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func namedRecord(name string) (Record, error) {
	s, err := OpenStore()
	if err != nil {
		return Record{}, err
	}
	return s.read(name)
}
func ensureNamedBridge(ctx context.Context, r Record) error {
	devices, err := links(ctx)
	if err != nil {
		return err
	}
	exists := false
	for _, d := range devices {
		if d.Name == r.Bridge {
			if d.Info.Kind != "bridge" || d.Group != ownershipGroup("casklet named "+r.Name) {
				return errors.New("named bridge exists without casklet ownership")
			}
			exists = true
		}
	}
	if !exists {
		out, err := command(ctx, "ip", "", "-j", "-4", "route", "show", "table", "all")
		if err != nil {
			return err
		}
		if err := checkNamedRoutes(out, r.Subnet); err != nil {
			return err
		}
		if err := addLink(r.Bridge, "casklet named "+r.Name, "bridge", ""); err != nil {
			return err
		}
	}
	if _, err := command(ctx, "ip", "", "link", "set", r.Bridge, "alias", "casklet named "+r.Name); err != nil {
		return err
	}
	if _, err := command(ctx, "ip", "", "address", "replace", r.Gateway+"/24", "dev", r.Bridge); err != nil {
		return err
	}
	if _, err := command(ctx, "ip", "", "link", "set", r.Bridge, "up"); err != nil {
		return err
	}
	return os.WriteFile("/proc/sys/net/ipv4/conf/"+r.Bridge+"/route_localnet", []byte("1\n"), 0600)
}
func removeNamedBridge(ctx context.Context, r Record) error {
	devices, err := links(ctx)
	if err != nil {
		return err
	}
	for _, d := range devices {
		if d.Name == r.Bridge {
			if d.Info.Kind != "bridge" || d.Group != ownershipGroup("casklet named "+r.Name) {
				return errors.New("refuse cleanup of an unowned named bridge")
			}
			_, err := command(ctx, "ip", "", "link", "delete", r.Bridge)
			return err
		}
	}
	return nil
}

func protectNamedForwarding() error {
	p := filepath.Join(runsRoot, ".network-shared.json")
	var state sharedState
	if err := readJSON(p, &state); err != nil {
		return err
	}
	path := filepath.Join(runsRoot, ".named-forwarding.json")
	var saved sharedState
	err := readJSON(path, &saved)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil && (!validBoot(saved.BootID) || (saved.Forwarding != "0" && saved.Forwarding != "1")) {
		return errors.New("invalid named forwarding journal")
	}
	if errors.Is(err, os.ErrNotExist) || saved.BootID != state.BootID {
		if err := writeJSON(path, state); err != nil {
			return err
		}
	}
	state.Forwarding = "1"
	return writeJSON(p, state)
}

func restoreNamedForwarding() error {
	path := filepath.Join(runsRoot, ".named-forwarding.json")
	var state sharedState
	if err := readJSON(path, &state); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	boot, err := currentBoot()
	if err != nil {
		return err
	}
	if !validBoot(state.BootID) || (state.Forwarding != "0" && state.Forwarding != "1") {
		return errors.New("invalid named forwarding journal")
	}
	if state.BootID == boot && state.Forwarding == "0" {
		if err := os.WriteFile(forwardingPath, []byte("0\n"), 0600); err != nil {
			return err
		}
	}
	return os.Remove(path)
}
func checkNamedRoutes(data []byte, subnet string) error {
	var routes []struct {
		Destination string `json:"dst"`
	}
	if err := json.Unmarshal(data, &routes); err != nil {
		return err
	}
	pool := netip.MustParsePrefix(subnet)
	for _, r := range routes {
		p, err := netip.ParsePrefix(r.Destination)
		if err != nil {
			if ip, err := netip.ParseAddr(r.Destination); err == nil && pool.Contains(ip) {
				return errors.New("named network subnet overlaps a host route")
			}
			continue
		}
		if p.Bits() > 0 && (p.Contains(pool.Addr()) || pool.Contains(p.Addr())) {
			return errors.New("named network subnet overlaps a host route")
		}
	}
	return nil
}
