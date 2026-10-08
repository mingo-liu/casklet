//go:build linux

// Package network owns opt-in IPv4 bridge networking and its recovery journal.
package network

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mingo-liu/casklet/internal/config"
	"golang.org/x/sys/unix"
)

const (
	bridge         = "casklet0"
	gateway        = "10.231.0.1"
	subnet         = "10.231.0.0/24"
	bridgeAlias    = "casklet bridge v1"
	runsRoot       = "/var/lib/casklet/runs"
	forwardingPath = "/proc/sys/net/ipv4/ip_forward"
)

type allocation struct {
	BootID  string               `json:"boot_id"`
	Address string               `json:"address"`
	Publish []config.PortMapping `json:"publish"`
}

type sharedState struct {
	BootID     string `json:"boot_id"`
	Forwarding string `json:"forwarding"`
}

// Lease keeps host ports reserved until NAT and interfaces have been removed.
type Lease struct{ sockets []io.Closer }

func (l *Lease) Close() {
	for _, socket := range l.sockets {
		socket.Close()
	}
	l.sockets = nil
}

func currentBoot() (string, error) {
	data, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(data))
	if !validBoot(id) {
		return "", errors.New("invalid host boot identity")
	}
	return id, nil
}

func validBoot(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, ch := range id {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if ch != '-' {
				return false
			}
			continue
		}
		if !strings.ContainsRune("0123456789abcdef", ch) {
			return false
		}
	}
	return true
}

func identity(path string) (string, string) {
	sum := sha256.Sum256([]byte(filepath.Base(path)))
	suffix := hex.EncodeToString(sum[:])[:12]
	return "cs" + suffix, "casklet_" + suffix
}

func checkPath(path string) error {
	if filepath.Dir(path) != runsRoot || filepath.Clean(path) != path || !strings.HasPrefix(filepath.Base(path), "run-") || len(filepath.Base(path)) <= 4 {
		return errors.New("invalid network run directory")
	}
	for _, dir := range []string{"/var/lib/casklet", runsRoot, path} {
		var stat unix.Stat_t
		if err := unix.Lstat(dir, &stat); err != nil {
			return err
		}
		if stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Uid != 0 || stat.Mode&0022 != 0 {
			return errors.New("network state requires private root-owned directories")
		}
	}
	return nil
}

func lock(ctx context.Context) (*os.File, error) {
	fd, err := unix.Open(filepath.Join(runsRoot, ".network.lock"), unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "network-lock")
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Uid != 0 || stat.Mode&0022 != 0 || stat.Nlink != 1 {
		file.Close()
		return nil, errors.New("invalid network coordination lock")
	}
	for {
		if err := ctx.Err(); err != nil {
			file.Close()
			return nil, err
		}
		err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return file, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EINTR) {
			file.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			file.Close()
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func readJSON(path string, value any) error {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), "network-state")
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 32768 || stat.Uid != 0 || stat.Mode&0022 != 0 || stat.Nlink != 1 {
		return errors.New("invalid network state file")
	}
	decoder := json.NewDecoder(io.LimitReader(file, 32769))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("trailing network state data")
	}
	return nil
}

func writeJSON(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".network-state-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(data)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// Commands use fixed system paths, a bounded deadline, and no invoking-user environment.
func tool(name string) (string, error) {
	for _, dir := range []string{"/usr/sbin", "/usr/bin", "/sbin", "/bin"} {
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if err == nil && !info.IsDir() && info.Mode()&0111 != 0 {
			return path, nil
		}
	}
	return "", fmt.Errorf("bridge networking requires %s (install iproute2, nftables, util-linux, and conntrack)", name)
}

func command(ctx context.Context, name, input string, args ...string) ([]byte, error) {
	return commandFiles(ctx, nil, name, input, args...)
}

func commandFiles(ctx context.Context, files []*os.File, name, input string, args ...string) ([]byte, error) {
	executable, err := tool(name)
	if err != nil {
		return nil, err
	}
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(bounded, executable, args...)
	cmd.ExtraFiles = files
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "LC_ALL=C"}
	if input != "" {
		cmd.Stdin = strings.NewReader(input)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func Resolvers(explicit []string) ([]string, error) {
	if len(explicit) != 0 {
		return append([]string(nil), explicit...), config.ValidateNetwork("bridge", explicit, nil, nil)
	}
	for _, path := range []string{"/run/systemd/resolve/resolv.conf", "/etc/resolv.conf"} {
		file, err := os.Open(path)
		if err != nil {
			continue
		}
		servers := ParseResolvers(file)
		file.Close()
		if len(servers) != 0 {
			return servers, nil
		}
	}
	return nil, errors.New("no usable host IPv4 DNS servers; specify --dns with --network bridge")
}

func allocations() (map[string]allocation, error) {
	entries, err := os.ReadDir(runsRoot)
	if err != nil {
		return nil, err
	}
	result := map[string]allocation{}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "run-") {
			continue
		}
		path := filepath.Join(runsRoot, entry.Name())
		if err := checkPath(path); err != nil {
			// A loopback-only run may disappear while the directory listing is read.
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		var record allocation
		err := readJSON(filepath.Join(path, "network.json"), &record)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !validBoot(record.BootID) {
			return nil, errors.New("invalid recorded network boot identity")
		}
		ip, err := netip.ParseAddr(record.Address)
		if err != nil || !netip.MustParsePrefix(subnet).Contains(ip) || ip.As4()[3] < 2 || ip.As4()[3] == 255 {
			return nil, errors.New("invalid recorded network address")
		}
		if err := config.ValidateNetwork("bridge", nil, record.Publish, nil); err != nil {
			return nil, err
		}
		result[path] = record
	}
	return result, nil
}

type linkInfo struct {
	Name  string `json:"ifname"`
	Alias string `json:"ifalias"`
	Group uint32 `json:"group,string"`
	Info  struct {
		Kind string `json:"info_kind"`
	} `json:"linkinfo"`
}

func links(ctx context.Context) ([]linkInfo, error) {
	out, err := command(ctx, "ip", "", "-N", "-j", "-d", "link", "show")
	if err != nil {
		return nil, err
	}
	var result []linkInfo
	err = json.Unmarshal(out, &result)
	return result, err
}

func ensureBridge(ctx context.Context) error {
	boot, err := currentBoot()
	if err != nil {
		return err
	}
	statePath := filepath.Join(runsRoot, ".network-shared.json")
	var state sharedState
	err = readJSON(statePath, &state)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	fresh := errors.Is(err, os.ErrNotExist)
	if !fresh && state.BootID != boot {
		return errors.New("previous-boot network state requires recovery")
	}
	if !fresh && state.Forwarding != "0" && state.Forwarding != "1" {
		return errors.New("invalid recorded IP forwarding setting")
	}
	devices, err := links(ctx)
	if err != nil {
		return err
	}
	exists := false
	for _, device := range devices {
		if device.Name == bridge {
			if fresh || device.Group != ownershipGroup(bridgeAlias) || device.Info.Kind != "bridge" {
				return errors.New("casklet0 already exists or is not owned by casklet")
			}
			exists = true
		}
	}
	if fresh {
		out, err := command(ctx, "ip", "", "-j", "-4", "route", "show", "table", "all")
		if err != nil {
			return err
		}
		var routes []struct {
			Destination string `json:"dst"`
		}
		if err := json.Unmarshal(out, &routes); err != nil {
			return err
		}
		pool := netip.MustParsePrefix(subnet)
		for _, route := range routes {
			prefix, err := netip.ParsePrefix(route.Destination)
			if err != nil {
				if ip, err := netip.ParseAddr(route.Destination); err == nil && pool.Contains(ip) {
					return errors.New("bridge subnet 10.231.0.0/24 overlaps a host route")
				}
				continue
			}
			if prefix.Bits() > 0 && (prefix.Contains(pool.Addr()) || pool.Contains(prefix.Addr())) {
				return errors.New("bridge subnet 10.231.0.0/24 overlaps a host route")
			}
		}
		value, err := os.ReadFile(forwardingPath)
		if err != nil {
			return err
		}
		state.BootID = boot
		state.Forwarding = strings.TrimSpace(string(value))
		if state.Forwarding != "0" && state.Forwarding != "1" {
			return errors.New("invalid host IP forwarding setting")
		}
		// Persist intent before the first host mutation so recovery covers interrupted setup.
		if err := writeJSON(statePath, state); err != nil {
			return err
		}
	}
	if !exists {
		// Create and assign ownership in the same netlink request.
		if err := addLink(bridge, bridgeAlias, "bridge", ""); err != nil {
			return err
		}
		if _, err := command(ctx, "ip", "", "link", "set", bridge, "alias", bridgeAlias); err != nil {
			return err
		}
	}
	if _, err := command(ctx, "ip", "", "address", "replace", gateway+"/24", "dev", bridge); err != nil {
		return err
	}
	if _, err := command(ctx, "ip", "", "link", "set", bridge, "up"); err != nil {
		return err
	}
	// Only this owned bridge may route loopback-source packets for local DNAT.
	if err := os.WriteFile("/proc/sys/net/ipv4/conf/"+bridge+"/route_localnet", []byte("1\n"), 0600); err != nil {
		return err
	}
	return os.WriteFile(forwardingPath, []byte("1\n"), 0600)
}

func containerMAC(address string) string {
	ip := netip.MustParseAddr(address).As4()
	return fmt.Sprintf("02:4d:%02x:%02x:%02x:%02x", ip[0], ip[1], ip[2], ip[3])
}

func reserve(p config.PortMapping) (io.Closer, error) {
	addr := net.JoinHostPort(p.HostIP, strconv.Itoa(int(p.HostPort)))
	if p.Protocol == "udp" {
		return net.ListenPacket("udp4", addr)
	}
	return net.Listen("tcp4", addr)
}

// Setup journals allocation before attaching a veth to the init's live namespace.
// On every error, the caller must invoke Cleanup before closing any returned
// lease; failed cleanup preserves its journal.
func Setup(ctx context.Context, path string, namespace *os.File, ports []config.PortMapping) (resultLease *Lease, resultErr error) {
	if namespace == nil {
		return nil, errors.New("network setup requires a pinned namespace")
	}
	if err := checkPath(path); err != nil {
		return nil, err
	}
	if err := config.ValidateNetwork("bridge", nil, ports, nil); err != nil {
		return nil, err
	}
	for _, name := range []string{"ip", "nft", "nsenter", "conntrack"} {
		if _, err := tool(name); err != nil {
			return nil, err
		}
	}
	coordination, err := lock(ctx)
	if err != nil {
		return nil, err
	}
	defer coordination.Close()
	records, err := allocations()
	if err != nil {
		return nil, err
	}
	if _, exists := records[path]; exists {
		return nil, errors.New("network allocation already exists")
	}
	used := map[string]bool{}
	for _, record := range records {
		used[record.Address] = true
		for _, requested := range ports {
			for _, existing := range record.Publish {
				if config.PortsConflict(requested, existing) {
					return nil, errors.New("published host port is already allocated")
				}
			}
		}
	}
	address := ""
	for n := 2; n < 255; n++ {
		candidate := "10.231.0." + strconv.Itoa(n)
		if !used[candidate] {
			address = candidate
			break
		}
	}
	if address == "" {
		return nil, errors.New("bridge address pool is exhausted")
	}
	lease := &Lease{}
	success, journaled := false, false
	defer func() {
		if !success {
			if journaled {
				// The caller retains reservations through rollback, even if a command
				// failed after the kernel applied some of its network mutations.
				resultLease = lease
			} else {
				lease.Close()
			}
		}
	}()
	for _, port := range ports {
		socket, err := reserve(port)
		if err != nil {
			return nil, fmt.Errorf("reserve %s host port %s:%d: %w", port.Protocol, port.HostIP, port.HostPort, err)
		}
		lease.sockets = append(lease.sockets, socket)
	}
	host, table := identity(path)
	devices, err := links(ctx)
	if err != nil {
		return nil, err
	}
	for _, device := range devices {
		if device.Name == host || device.Name == host+"p" {
			return nil, errors.New("network interface name conflict")
		}
	}
	if _, exists, err := ownedTable(ctx, path); err != nil || exists {
		if err == nil {
			err = errors.New("network table name conflict")
		}
		return nil, err
	}
	if err := ensureBridge(ctx); err != nil {
		return nil, err
	}
	boot, err := currentBoot()
	if err != nil {
		return nil, err
	}
	if err := writeJSON(filepath.Join(path, "network.json"), allocation{BootID: boot, Address: address, Publish: ports}); err != nil {
		return nil, err
	}
	journaled = true
	if err := addLink(host, table, "veth", host+"p"); err != nil {
		return nil, err
	}
	if _, err := command(ctx, "ip", "", "link", "set", host, "alias", table); err != nil {
		return nil, err
	}
	if _, err := command(ctx, "ip", "", "link", "set", host, "master", bridge); err != nil {
		return nil, err
	}
	if _, err := command(ctx, "ip", "", "link", "set", host, "up"); err != nil {
		return nil, err
	}
	if _, err := commandFiles(ctx, []*os.File{namespace}, "ip", "", "link", "set", host+"p", "netns", "/proc/self/fd/3"); err != nil {
		return nil, err
	}
	ipTool, _ := tool("ip")
	for _, args := range [][]string{
		{"link", "set", host + "p", "name", "eth0"},
		// A stable MAC per leased address keeps peers' neighbor caches valid on reuse.
		{"link", "set", "eth0", "address", containerMAC(address)},
		{"address", "add", address + "/24", "dev", "eth0"},
		{"link", "set", "eth0", "up"},
		{"route", "add", "default", "via", gateway},
	} {
		args = append([]string{"--net=/proc/self/fd/3", "--", ipTool}, args...)
		if _, err := commandFiles(ctx, []*os.File{namespace}, "nsenter", "", args...); err != nil {
			return nil, err
		}
	}
	if _, err := command(ctx, "nft", rules(path, address, ports), "-f", "-"); err != nil {
		return nil, err
	}
	success = true
	return lease, nil
}

func rules(path, address string, ports []config.PortMapping) string {
	_, table := identity(path)
	var result strings.Builder
	fmt.Fprintf(&result, "create table ip %s { comment \"%s\"; }\nadd table ip %s {\n", table, table, table)
	result.WriteString("chain prerouting { type nat hook prerouting priority dstnat; policy accept;\n")
	for _, p := range ports {
		// Loopback mappings are host-local and must never DNAT ingress packets.
		if netip.MustParseAddr(p.HostIP).IsLoopback() {
			continue
		}
		result.WriteString(portRule(p, address))
	}
	result.WriteString("}\nchain output { type nat hook output priority dstnat; policy accept;\n")
	for _, p := range ports {
		result.WriteString(portRule(p, address))
	}
	result.WriteString("}\nchain postrouting { type nat hook postrouting priority srcnat; policy accept;\n")
	// Masquerade outgoing traffic, and SNAT hairpin/localhost DNAT to this container.
	fmt.Fprintf(&result, "ip saddr %s oifname != \"%s\" counter masquerade\n", address, bridge)
	fmt.Fprintf(&result, "ip daddr %s ct status dnat oifname \"%s\" counter snat to %s\n", address, bridge, gateway)
	result.WriteString("}\nchain forward { type filter hook forward priority filter; policy accept;\n")
	fmt.Fprintf(&result, "iifname \"%s\" ip saddr %s accept\n", bridge, address)
	fmt.Fprintf(&result, "oifname \"%s\" ip daddr %s ct state established,related accept\n", bridge, address)
	for _, p := range ports {
		fmt.Fprintf(&result, "oifname \"%s\" ip daddr %s %s dport %d ct status dnat accept\n", bridge, address, p.Protocol, p.ContainerPort)
	}
	result.WriteString("}\n}\n")
	return result.String()
}

func portRule(p config.PortMapping, address string) string {
	match := "fib daddr type local"
	if p.HostIP != "0.0.0.0" {
		match = "ip daddr " + p.HostIP
	}
	return fmt.Sprintf("%s %s dport %d counter dnat to %s:%d\n", match, p.Protocol, p.HostPort, address, p.ContainerPort)
}

func ownedTable(ctx context.Context, path string) (string, bool, error) {
	_, table := identity(path)
	out, err := command(ctx, "nft", "", "-j", "list", "tables")
	if err != nil {
		return table, false, err
	}
	var listing struct {
		Items []struct {
			Table *struct {
				Family string `json:"family"`
				Name   string `json:"name"`
			} `json:"table"`
		} `json:"nftables"`
	}
	if err := json.Unmarshal(out, &listing); err != nil {
		return table, false, err
	}
	for _, item := range listing.Items {
		if item.Table == nil || item.Table.Family != "ip" || item.Table.Name != table {
			continue
		}
		out, err := command(ctx, "nft", "", "-j", "list", "table", "ip", table)
		if err != nil {
			return table, false, err
		}
		var detail struct {
			Items []struct {
				Table *struct {
					Comment string `json:"comment"`
				} `json:"table"`
			} `json:"nftables"`
		}
		if err := json.Unmarshal(out, &detail); err != nil {
			return table, false, err
		}
		for _, item := range detail.Items {
			if item.Table != nil && item.Table.Comment == table {
				return table, true, nil
			}
		}
		return table, true, errors.New("refuse cleanup of an unowned nftables table")
	}
	return table, false, nil
}

// Cleanup is idempotent, serialized against new allocations, and safe after
// partial startup or supervisor loss. Its journal survives every failed cleanup.
func Cleanup(path string) error {
	if err := checkPath(path); err != nil {
		return err
	}
	// Loopback-only runs have no dependency on networking tools or state.
	_, allocationErr := os.Lstat(filepath.Join(path, "network.json"))
	_, sharedErr := os.Lstat(filepath.Join(runsRoot, ".network-shared.json"))
	if errors.Is(allocationErr, os.ErrNotExist) && errors.Is(sharedErr, os.ErrNotExist) {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	coordination, err := lock(ctx)
	if err != nil {
		return err
	}
	defer coordination.Close()
	records, err := allocations()
	if err != nil {
		return err
	}
	boot, err := currentBoot()
	if err != nil {
		return err
	}
	if record, exists := records[path]; exists && record.BootID == boot {
		table, present, err := ownedTable(ctx, path)
		if err != nil {
			return err
		}
		if present {
			if _, err := command(ctx, "nft", "", "delete", "table", "ip", table); err != nil {
				return err
			}
		}
		devices, err := links(ctx)
		if err != nil {
			return err
		}
		host, _ := identity(path)
		for _, device := range devices {
			if device.Name != host {
				continue
			}
			if device.Group != ownershipGroup(table) || device.Info.Kind != "veth" {
				return errors.New("refuse cleanup of an unowned network interface")
			}
			if _, err := command(ctx, "ip", "", "link", "delete", host); err != nil {
				// Namespace destruction can remove the peer after the link dump.
				current, listErr := links(ctx)
				if listErr != nil {
					return errors.Join(err, listErr)
				}
				for _, link := range current {
					if link.Name == host {
						return err
					}
				}
			}
		}
		// NAT connection state otherwise survives rule deletion and address reuse.
		for _, selector := range []string{"--orig-src", "--orig-dst", "--reply-src", "--reply-dst"} {
			out, err := command(ctx, "conntrack", "", "-D", "-f", "ipv4", selector, record.Address)
			if err != nil && !strings.Contains(string(out), "0 flow entries have been deleted") {
				return err
			}
		}
		// Remove the host's cached neighbor before this address is leased again.
		if _, err := command(ctx, "ip", "", "neigh", "flush", "to", record.Address, "dev", bridge); err != nil {
			devices, listErr := links(ctx)
			if listErr != nil {
				return errors.Join(err, listErr)
			}
			for _, device := range devices {
				if device.Name == bridge {
					return err
				}
			}
		}
	}
	if _, exists := records[path]; exists {
		if err := os.Remove(filepath.Join(path, "network.json")); err != nil {
			return err
		}
		delete(records, path)
	}
	if len(records) != 0 {
		return nil
	}
	return cleanupBridge(ctx)
}

func cleanupBridge(ctx context.Context) error {
	path := filepath.Join(runsRoot, ".network-shared.json")
	var state sharedState
	err := readJSON(path, &state)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !validBoot(state.BootID) {
		return errors.New("invalid recorded bridge boot identity")
	}
	if state.Forwarding != "0" && state.Forwarding != "1" {
		return errors.New("invalid recorded forwarding setting")
	}
	boot, err := currentBoot()
	if err != nil {
		return err
	}
	// Interfaces, nftables tables, and conntrack entries do not survive reboot.
	// Old journals must not alter a newly configured host's resources or sysctls.
	if state.BootID != boot {
		return os.Remove(path)
	}
	devices, err := links(ctx)
	if err != nil {
		return err
	}
	for _, device := range devices {
		if device.Name != bridge {
			continue
		}
		if device.Group != ownershipGroup(bridgeAlias) || device.Info.Kind != "bridge" {
			return errors.New("refuse cleanup of an unowned bridge")
		}
		if _, err := command(ctx, "ip", "", "link", "delete", bridge); err != nil {
			return err
		}
	}
	if state.Forwarding == "0" {
		value, err := os.ReadFile(forwardingPath)
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(value)) == "1" {
			if err := os.WriteFile(forwardingPath, []byte("0\n"), 0600); err != nil {
				return err
			}
		}
	}
	return os.Remove(path)
}
