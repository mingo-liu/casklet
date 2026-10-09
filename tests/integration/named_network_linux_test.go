//go:build linux

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/mingo-liu/casklet/internal/network"
	"golang.org/x/sys/unix"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func namedNetwork(t *testing.T) string {
	t.Helper()
	name := fmt.Sprintf("named-%d-%d", time.Now().UnixNano(), sequence.Add(1))
	backgroundSuccess(t, "network", "create", name)
	t.Cleanup(func() { backgroundCLI(t, "network", "rm", name) })
	return name
}
func namedAddress(t *testing.T, id string) string {
	t.Helper()
	fields := strings.Fields(backgroundSuccess(t, "exec", id, "--", "/bin/ip", "-4", "addr", "show", "dev", "eth0"))
	for i, f := range fields {
		if f == "inet" {
			return strings.Split(fields[i+1], "/")[0]
		}
	}
	t.Fatal("no network address")
	return ""
}
func TestNamedNetworkDiscoveryIsolationRestartAndReferences(t *testing.T) {
	require(t)
	before := networkSnapshot(t)
	one, two := namedNetwork(t), namedNetwork(t)
	// Register the final resource comparison before members so cleanup runs first.
	t.Cleanup(func() { assertNetworkSnapshot(t, before) })
	a := startBackground(t, backgroundName(t), []string{"--network", one, "--network-alias", "redis"}, "/bin/integration-helper", "network-service")
	b := startBackground(t, backgroundName(t), []string{"--network", two, "--network-alias", "redis"}, "/bin/integration-helper", "network-service")
	consumer := startBackground(t, backgroundName(t), []string{"--network", one}, "/bin/sleep", "300")
	aa, bb := namedAddress(t, a), namedAddress(t, b)
	if aa == bb {
		t.Fatal("named networks share a subnet")
	}
	for _, protocol := range []string{"udp", "tcp"} {
		if out := strings.TrimSpace(backgroundSuccess(t, "exec", consumer, "--", "/bin/integration-helper", "network-resolve", protocol, "redis")); out != aa {
			t.Fatalf("%s lookup: %s != %s", protocol, out, aa)
		}
	}
	if out := strings.TrimSpace(backgroundSuccess(t, "exec", b, "--", "/bin/integration-helper", "network-resolve", "udp", "redis.casklet")); out != bb {
		t.Fatalf("scope leak: %s != %s", out, bb)
	}
	// Same network communicates; direct IP routing between networks must fail.
	if out := backgroundSuccess(t, "exec", consumer, "--", "/bin/wget", "-T", "2", "-qO-", "http://redis:8080/"); !strings.Contains(out, "network-ok") {
		t.Fatal(out)
	}
	if code, out, err := backgroundCLI(t, "exec", consumer, "--", "/bin/wget", "-T", "1", "-qO-", "http://"+bb+":8080/"); code == 0 {
		t.Fatalf("cross-network access: %s %s", out, err)
	}
	// Duplicate aliases include stopped retained containers, not just live journals.
	backgroundSuccess(t, "stop", a)
	duplicate := backgroundName(t)
	if code, _, _ := backgroundCLI(t, detachedArguments(duplicate, []string{"--network", one, "--network-alias", "redis"}, "/bin/true")...); code != 125 {
		t.Fatal("accepted duplicate retained alias")
	}
	if code, _, _ := backgroundCLI(t, "network", "rm", one); code != 125 {
		t.Fatal("removed referenced network")
	}
	if code, _, _ := backgroundCLI(t, "exec", consumer, "--", "/bin/integration-helper", "network-resolve", "udp", "redis"); code == 0 {
		t.Fatal("stopped execution still resolves")
	}
	// Occupy the released address to force the next generation to use another IP.
	blocker := startBackground(t, backgroundName(t), []string{"--network", one}, "/bin/sleep", "300")
	backgroundSuccess(t, "start", a)
	fresh := namedAddress(t, a)
	if fresh == aa {
		t.Fatal("restart fixture did not allocate a new address")
	}
	if out := strings.TrimSpace(backgroundSuccess(t, "exec", consumer, "--", "/bin/integration-helper", "network-resolve", "tcp", "redis")); out != fresh {
		t.Fatalf("stale discovery after restart %s != %s", out, fresh)
	}
	for _, id := range []string{a, b, consumer, blocker} {
		backgroundSuccess(t, "stop", id)
		backgroundSuccess(t, "rm", id)
	}
	backgroundSuccess(t, "network", "rm", one)
	backgroundSuccess(t, "network", "rm", two)
}
func TestNamedNetworkUpstreamDNSPublishAndForegroundLease(t *testing.T) {
	require(t)
	before := networkSnapshot(t)
	networkUpstream(t)
	name := namedNetwork(t)
	code, out, stderr := start(t, "", "run", "--rootfs", template, "--network", name, "--dns", "198.18.0.2", "--", "/bin/integration-helper", "network-client").wait(t)
	if code != 0 || !strings.Contains(out, "network-ok") {
		t.Fatalf("named upstream: %d %s %s", code, out, stderr)
	}
	for _, protocol := range []string{"udp", "tcp"} {
		code, out, stderr := start(t, "", "run", "--rootfs", template, "--network", name, "--dns", "198.18.0.2", "--", "/bin/integration-helper", "network-resolve", protocol, "fixture.test").wait(t)
		if code != 0 || strings.TrimSpace(out) != "198.18.0.2" {
			t.Fatalf("upstream %s: %d %s %s", protocol, code, out, stderr)
		}
	}
	port := freeNetworkPort(t, "tcp")
	id := startBackground(t, backgroundName(t), []string{"--network", name, "-p", fmt.Sprintf("127.0.0.1:%d:8080", port)}, "/bin/integration-helper", "network-service")
	networkHTTP(t, fmt.Sprintf("127.0.0.1:%d", port))
	backgroundSuccess(t, "stop", id)
	backgroundSuccess(t, "rm", id)
	backgroundSuccess(t, "network", "rm", name)
	assertNetworkSnapshot(t, before)
}
func TestNamedNetworkMetadataAndAliasValidation(t *testing.T) {
	require(t)
	name := namedNetwork(t)
	var record network.Record
	if err := json.Unmarshal([]byte(backgroundSuccess(t, "network", "inspect", name)), &record); err != nil {
		t.Fatal(err)
	}
	if record.Name != name || !strings.HasPrefix(record.Bridge, "csn") || !strings.HasPrefix(record.Subnet, "10.232.") {
		t.Fatalf("invalid metadata: %+v", record)
	}
	backgroundSuccess(t, "network", "create", name)
	for _, options := range [][]string{{"--network", name, "--name", "invalid_name"}, {"--network", name, "--network-alias", "Bad"}, {"--network", name, "--network-alias", "same", "--network-alias", "same"}} {
		args := append([]string{"run", "-d", "--rootfs", template}, options...)
		args = append(args, "--", "/bin/true")
		if code, _, _ := backgroundCLI(t, args...); code != 125 {
			t.Fatalf("accepted invalid named args %v", args)
		}
	}
}

func TestNamedNetworkForegroundLease(t *testing.T) {
	require(t)
	name := namedNetwork(t)
	call := start(t, "", "run", "--rootfs", template, "--network", name, "--", "/bin/sh", "-c", "echo ready; sleep 300")
	pid := call.supervisor(t)
	if code, _, _ := backgroundCLI(t, "network", "rm", name); code != 125 {
		t.Fatal("removed active foreground network")
	}
	if err := unix.Kill(pid, unix.SIGTERM); err != nil {
		t.Fatal(err)
	}
	call.wait(t)
	backgroundSuccess(t, "network", "rm", name)
}
func TestNamedNetworkBridgeCleanupFailurePreservesJournal(t *testing.T) {
	require(t)
	name := namedNetwork(t)
	var meta network.Record
	json.Unmarshal([]byte(backgroundSuccess(t, "network", "inspect", name)), &meta)
	id := startBackground(t, backgroundName(t), []string{"--network", name}, "/bin/sleep", "300")
	record := waitBackground(t, id, "running")
	var links []struct {
		Group uint32 `json:"group,string"`
	}
	json.Unmarshal([]byte(networkCommand(t, "ip", "-N", "-j", "-d", "link", "show", meta.Bridge)), &links)
	if len(links) != 1 {
		t.Fatal("missing named bridge")
	}
	group := strconv.FormatUint(uint64(links[0].Group), 10)
	t.Cleanup(func() { exec.Command("ip", "link", "set", meta.Bridge, "group", group).Run() })
	networkCommand(t, "ip", "link", "set", meta.Bridge, "group", "0")
	code, _, diagnostic := backgroundCLI(t, "stop", id)
	if code != 125 || !strings.Contains(diagnostic, "unowned named bridge") {
		t.Fatalf("faulted cleanup: %d %s", code, diagnostic)
	}
	journal := filepath.Join(record.RunPath, "network-named.json")
	if _, err := os.Stat(journal); err != nil {
		t.Fatalf("cleanup failure lost journal: %v", err)
	}
	if err := network.Cleanup(record.RunPath); err == nil {
		t.Fatal("cleanup accepted unowned bridge")
	}
	if _, err := os.Stat(journal); err != nil {
		t.Fatal("retry lost recovery receipt")
	}
	networkCommand(t, "ip", "link", "set", meta.Bridge, "group", group)
	if err := network.Cleanup(record.RunPath); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(journal); !os.IsNotExist(err) {
		t.Fatalf("successful cleanup left journal: %v", err)
	}
	backgroundSuccess(t, "rm", id)
	backgroundSuccess(t, "network", "rm", name)
}
func TestNamedNetworkForwardingCleanupOrders(t *testing.T) {
	require(t)
	if _, err := os.Stat("/var/lib/casklet/runs/.network-shared.json"); !os.IsNotExist(err) {
		t.Skip("requires no active bridge workloads")
	}
	path := "/proc/sys/net/ipv4/ip_forward"
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.WriteFile(path, saved, 0600) })
	for _, namedFirst := range []bool{true, false} {
		os.WriteFile(path, []byte("0\n"), 0600)
		name := namedNetwork(t)
		legacy := startBackground(t, backgroundName(t), []string{"--network", "bridge"}, "/bin/sleep", "300")
		named := startBackground(t, backgroundName(t), []string{"--network", name}, "/bin/sleep", "300")
		first, last := legacy, named
		if namedFirst {
			first, last = named, legacy
		}
		backgroundSuccess(t, "stop", first)
		value, _ := os.ReadFile(path)
		if strings.TrimSpace(string(value)) != "1" {
			t.Fatal("forwarding disabled while a network is active")
		}
		backgroundSuccess(t, "stop", last)
		value, _ = os.ReadFile(path)
		if strings.TrimSpace(string(value)) != "0" {
			t.Fatal("original forwarding setting was not restored")
		}
		backgroundSuccess(t, "rm", first)
		backgroundSuccess(t, "rm", last)
		backgroundSuccess(t, "network", "rm", name)
	}
}

// Supply a baseline Linux engine to exercise a supervisor pinned before upgrade.
func TestNamedNetworkPinnedLegacySupervisor(t *testing.T) {
	require(t)
	legacyEngine := os.Getenv("CASKLET_LEGACY_ENGINE")
	if legacyEngine == "" {
		t.Skip("set CASKLET_LEGACY_ENGINE to a pre-named-network Linux engine")
	}
	if _, err := os.Stat("/var/lib/casklet/runs/.network-shared.json"); !os.IsNotExist(err) {
		t.Skip("requires no active bridge workloads")
	}
	forwarding := "/proc/sys/net/ipv4/ip_forward"
	saved, err := os.ReadFile(forwarding)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.WriteFile(forwarding, saved, 0600) })
	os.WriteFile(forwarding, []byte("0\n"), 0600)
	networkUpstream(t)
	legacyName := backgroundName(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, legacyEngine, detachedArguments(legacyName, []string{"--network", "bridge"}, "/bin/sleep", "300")...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("baseline launch: %v %s", err, out)
	}
	legacyID := backgroundID(t, string(out))
	name := namedNetwork(t)
	id := startBackground(t, backgroundName(t), []string{"--network", name, "--dns", "198.18.0.2"}, "/bin/sleep", "300")
	backgroundSuccess(t, "stop", legacyID)
	value, _ := os.ReadFile(forwarding)
	if strings.TrimSpace(string(value)) != "1" {
		t.Fatal("legacy cleanup disabled named forwarding")
	}
	if out := backgroundSuccess(t, "exec", id, "--", "/bin/integration-helper", "network-client"); !strings.Contains(out, "network-ok") {
		t.Fatal(out)
	}
	backgroundSuccess(t, "stop", id)
	value, _ = os.ReadFile(forwarding)
	if strings.TrimSpace(string(value)) != "0" {
		t.Fatal("mixed-generation cleanup lost original forwarding")
	}
	backgroundSuccess(t, "rm", legacyID)
	backgroundSuccess(t, "rm", id)
	backgroundSuccess(t, "network", "rm", name)
}
