//go:build linux

package integration

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mingo-liu/mini-docker/internal/config"
	"github.com/mingo-liu/mini-docker/internal/network"
)

func networkCommand(t *testing.T, tool string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, tool, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %q: %v: %s", tool, args, err, out)
	}
	return string(out)
}

func networkSnapshot(t *testing.T) string {
	t.Helper()
	var links []struct {
		Name  string `json:"ifname"`
		Alias string `json:"ifalias"`
	}
	if err := json.Unmarshal([]byte(networkCommand(t, "ip", "-j", "link", "show")), &links); err != nil {
		t.Fatal(err)
	}
	var owned []string
	for _, link := range links {
		if strings.HasPrefix(link.Alias, "mini-docker") || strings.HasPrefix(link.Alias, "mdocker_") {
			owned = append(owned, link.Name)
		}
	}
	tables := strings.Split(networkCommand(t, "nft", "list", "tables"), "\n")
	for _, table := range tables {
		if strings.Contains(table, "mdocker_") {
			owned = append(owned, table)
		}
	}
	entries, err := os.ReadDir("/var/lib/mini-docker/runs")
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if _, err := os.Lstat(filepath.Join("/var/lib/mini-docker/runs", entry.Name(), "network.json")); err == nil {
			owned = append(owned, entry.Name())
		}
	}
	if _, err := os.Lstat("/var/lib/mini-docker/runs/.network-shared.json"); err == nil {
		owned = append(owned, "shared-state")
	}
	sort.Strings(owned)
	value, err := os.ReadFile("/proc/sys/net/ipv4/ip_forward")
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(owned, ",") + " forwarding=" + strings.TrimSpace(string(value))
}

func assertNetworkSnapshot(t *testing.T, before string) {
	t.Helper()
	if after := networkSnapshot(t); after != before {
		t.Fatalf("network resources leaked: before=%s after=%s", before, after)
	}
}

// The upstream namespace has no route to the container subnet. Successful
// requests, with its observed source address, prove masquerading and DNS work.
func networkUpstream(t *testing.T) string {
	t.Helper()
	require(t)
	namespace := fmt.Sprintf("mdocker-upstream-%d-%d", os.Getpid(), sequence.Add(1))
	host := fmt.Sprintf("mx%d", sequence.Add(1))
	networkCommand(t, "ip", "netns", "add", namespace)
	t.Cleanup(func() {
		_ = exec.Command("ip", "netns", "delete", namespace).Run()
		_ = exec.Command("ip", "link", "delete", host).Run()
	})
	networkCommand(t, "ip", "link", "add", host, "type", "veth", "peer", "name", host+"p")
	networkCommand(t, "ip", "address", "add", "198.18.0.1/30", "dev", host)
	networkCommand(t, "ip", "link", "set", host, "up")
	networkCommand(t, "ip", "link", "set", host+"p", "netns", namespace)
	for _, args := range [][]string{{"link", "set", "lo", "up"}, {"address", "add", "198.18.0.2/30", "dev", host + "p"}, {"link", "set", host + "p", "up"}} {
		networkCommand(t, "ip", append([]string{"netns", "exec", namespace, "ip"}, args...)...)
	}
	// Intentionally no default route: return packets require host-side SNAT.
	cmd := exec.Command("ip", "netns", "exec", namespace, os.Getenv("MINI_DOCKER_HELPER"), "network-server")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	networkHTTP(t, "198.18.0.2:8080")
	return namespace
}

func networkHTTP(t *testing.T, addr string) string {
	t.Helper()
	client := &http.Client{Transport: &http.Transport{}, Timeout: 500 * time.Millisecond}
	deadline := time.Now().Add(5 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		response, err := client.Get("http://" + addr + "/")
		if err == nil {
			body, readErr := io.ReadAll(response.Body)
			response.Body.Close()
			if readErr == nil && strings.Contains(string(body), "network-ok") {
				return string(body)
			}
			last = readErr
		} else {
			last = err
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("HTTP %s unavailable: %v", addr, last)
	return ""
}

func freeNetworkPort(t *testing.T, protocol string) int {
	t.Helper()
	if protocol == "udp" {
		socket, err := net.ListenPacket("udp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := socket.LocalAddr().(*net.UDPAddr).Port
		socket.Close()
		return port
	}
	socket, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := socket.Addr().(*net.TCPAddr).Port
	socket.Close()
	return port
}

func TestNetworkConnectivityDNSAndReadOnly(t *testing.T) {
	require(t)
	before := networkSnapshot(t)
	networkUpstream(t)
	for _, options := range [][]string{nil, {"--read-only", "--user", "1234:1234"}} {
		args := []string{"run", "--rootfs", template, "--network", "bridge", "--dns", "198.18.0.2"}
		args = append(args, options...)
		args = append(args, "--", "/bin/integration-helper", "network-client")
		code, out, stderr := start(t, "", args...).wait(t)
		if code != 0 || !strings.Contains(out, "peer=198.18.0.1:") {
			t.Fatalf("NAT/DNS exit=%d out=%q stderr=%q", code, out, stderr)
		}
		assertNetworkSnapshot(t, before)
	}
	// Auto-discovery must skip the systemd loopback stub and generate a usable file.
	code, out, stderr := start(t, "", "run", "--rootfs", template, "--network", "bridge", "--", "/bin/cat", "/etc/resolv.conf").wait(t)
	if code != 0 || !strings.Contains(out, "nameserver ") || strings.Contains(out, "nameserver 127.") {
		t.Fatalf("automatic DNS: exit=%d out=%q stderr=%q", code, out, stderr)
	}
	assertNetworkSnapshot(t, before)
}

func TestNetworkPublishExecRestartAndConcurrency(t *testing.T) {
	require(t)
	before := networkSnapshot(t)
	tcp, udp := freeNetworkPort(t, "tcp"), freeNetworkPort(t, "udp")
	publishTCP := fmt.Sprintf("127.0.0.1:%d:8080", tcp)
	publishUDP := fmt.Sprintf("127.0.0.1:%d:7777/udp", udp)
	first := startBackground(t, backgroundName(t), []string{"--network", "bridge", "-p", publishTCP, "-p", publishUDP}, "/bin/integration-helper", "network-service")
	second := startBackground(t, backgroundName(t), []string{"--network", "bridge"}, "/bin/integration-helper", "network-service")
	networkHTTP(t, fmt.Sprintf("127.0.0.1:%d", tcp))
	address := func(id string) string {
		out := backgroundSuccess(t, "exec", id, "--", "/bin/ip", "-4", "addr", "show", "dev", "eth0")
		fields := strings.Fields(out)
		for i, field := range fields {
			if field == "inet" {
				return strings.Split(fields[i+1], "/")[0]
			}
		}
		t.Fatalf("no address: %s", out)
		return ""
	}
	a, b := address(first), address(second)
	if a == b {
		t.Fatal("concurrent containers share an IP")
	}
	networkHTTP(t, b+":8080")
	// Another container can reach the published port (hairpin NAT).
	code, out, stderr := backgroundCLI(t, "exec", second, "--", "/bin/wget", "-T", "3", "-qO-", fmt.Sprintf("http://10.231.0.1:%d", tcp))
	// This mapping is loopback-only, so it must not be exposed on the gateway.
	if code == 0 {
		t.Fatalf("loopback port exposed on gateway: %q %q", out, stderr)
	}
	packet, err := net.DialTimeout("udp4", fmt.Sprintf("127.0.0.1:%d", udp), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	packet.SetDeadline(time.Now().Add(2 * time.Second))
	_, err = packet.Write([]byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 64)
	n, err := packet.Read(buffer)
	packet.Close()
	if err != nil || string(buffer[:n]) != "echo:hello" {
		t.Fatalf("UDP mapping: %q %v", buffer[:n], err)
	}
	var inspection struct {
		Config struct {
			Network string               `json:"network"`
			Publish []config.PortMapping `json:"publish"`
		} `json:"config"`
	}
	if err := json.Unmarshal([]byte(backgroundSuccess(t, "inspect", first)), &inspection); err != nil || inspection.Config.Network != "bridge" || len(inspection.Config.Publish) != 2 {
		t.Fatalf("inspection=%+v error=%v", inspection, err)
	}
	failed := backgroundName(t)
	code, _, stderr = backgroundCLI(t, detachedArguments(failed, []string{"--network", "bridge", "-p", publishTCP}, "/bin/sleep", "30")...)
	if code != 125 || !strings.Contains(stderr, "port") {
		t.Fatalf("conflicting port: code=%d stderr=%s", code, stderr)
	}
	networkHTTP(t, fmt.Sprintf("127.0.0.1:%d", tcp))
	backgroundSuccess(t, "restart", "--timeout", "0s", first)
	networkHTTP(t, fmt.Sprintf("127.0.0.1:%d", tcp))
	backgroundSuccess(t, "stop", "--timeout", "0s", first)
	// Stopping the first container must preserve the second container and bridge.
	networkHTTP(t, b+":8080")
	backgroundSuccess(t, "stop", "--timeout", "0s", second)
	assertNetworkSnapshot(t, before)
}

func TestNetworkWildcardHairpinAndHostConflict(t *testing.T) {
	require(t)
	before := networkSnapshot(t)
	upstream := networkUpstream(t)
	port := freeNetworkPort(t, "tcp")
	mapping := strconv.Itoa(port) + ":8080"
	first := startBackground(t, backgroundName(t), []string{"--network", "bridge", "-p", mapping}, "/bin/integration-helper", "network-service")
	networkHTTP(t, fmt.Sprintf("127.0.0.1:%d", port))
	networkHTTP(t, fmt.Sprintf("10.231.0.1:%d", port))
	// Reach the published port from a genuinely different namespace.
	ingress := networkCommand(t, "ip", "netns", "exec", upstream, "curl", "--noproxy", "*", "--max-time", "3", "-fsS", fmt.Sprintf("http://198.18.0.1:%d/", port))
	if !strings.Contains(ingress, "network-ok") {
		t.Fatal(ingress)
	}
	code, out, stderr := backgroundCLI(t, "exec", first, "--", "/bin/wget", "-T", "3", "-qO-", fmt.Sprintf("http://10.231.0.1:%d", port))
	if code != 0 || !strings.Contains(out, "network-ok") {
		t.Fatalf("hairpin: code=%d out=%q stderr=%q", code, out, stderr)
	}
	backgroundSuccess(t, "stop", "--timeout", "0s", first)
	assertNetworkSnapshot(t, before)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	blockedPort := listener.Addr().(*net.TCPAddr).Port
	code, _, stderr = start(t, "", "run", "--rootfs", template, "--network", "bridge", "-p", fmt.Sprintf("%d:8080", blockedPort), "--", "/bin/true").wait(t)
	if code != 125 || !strings.Contains(stderr, "reserve") {
		t.Fatalf("host conflict code=%d stderr=%q", code, stderr)
	}
	assertNetworkSnapshot(t, before)
}

func TestNetworkStartupRollbackAndTimeout(t *testing.T) {
	require(t)
	before := networkSnapshot(t)
	for _, command := range [][]string{{"/missing"}, {"/bin/sleep", "30"}} {
		port := freeNetworkPort(t, "tcp")
		args := []string{"run", "--rootfs", template, "--network", "bridge", "-p", fmt.Sprintf("%d:8080", port), "--timeout", "100ms", "--stop-timeout", "0s", "--"}
		code, _, stderr := start(t, "", append(args, command...)...).wait(t)
		expected := 125
		if command[0] == "/bin/sleep" {
			expected = 124
		}
		if code != expected {
			t.Fatalf("exit=%d want=%d stderr=%q", code, expected, stderr)
		}
		assertNetworkSnapshot(t, before)
		socket, err := net.Listen("tcp4", fmt.Sprintf("0.0.0.0:%d", port))
		if err != nil {
			t.Fatalf("port retained: %v", err)
		}
		socket.Close()
	}
}

func TestNetworkConcurrentAllocationAndCleanup(t *testing.T) {
	require(t)
	before := networkSnapshot(t)
	var wg sync.WaitGroup
	errors := make(chan string, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			code, out, stderr, err := backgroundCommand(ctx, "run", "--rootfs", template, "--network", "bridge", "--", "/bin/sh", "-c", "ip -4 addr show dev eth0; sleep .2")
			if err != nil || code != 0 || !strings.Contains(out, "10.231.0.") {
				errors <- fmt.Sprintf("exit=%d out=%s stderr=%s err=%v", code, out, stderr, err)
			}
		}()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
	assertNetworkSnapshot(t, before)
}

func TestNetworkSupervisorRecovery(t *testing.T) {
	require(t)
	before := networkSnapshot(t)
	port := freeNetworkPort(t, "tcp")
	id := startBackground(t, backgroundName(t), []string{"--network", "bridge", "-p", fmt.Sprintf("127.0.0.1:%d:8080", port)}, "/bin/integration-helper", "network-service")
	networkHTTP(t, fmt.Sprintf("127.0.0.1:%d", port))
	networkCommand(t, "systemctl", "kill", "--kill-whom=main", "--signal=SIGKILL", "mini-docker-"+id+".service")
	waitBackground(t, id, "failed")
	assertBackgroundUnitStopped(t, id)
	assertNetworkSnapshot(t, before)
	// Manual start after recovery must reestablish the saved mapping.
	backgroundSuccess(t, "start", id)
	networkHTTP(t, fmt.Sprintf("127.0.0.1:%d", port))
	backgroundSuccess(t, "stop", "--timeout", "0s", id)
	assertNetworkSnapshot(t, before)
}

func TestNetworkRefusesHostConflicts(t *testing.T) {
	require(t)
	before := networkSnapshot(t)
	networkCommand(t, "ip", "link", "add", "mdocker0", "type", "dummy")
	t.Cleanup(func() { _ = exec.Command("ip", "link", "delete", "mdocker0").Run() })
	code, _, stderr := start(t, "", "run", "--rootfs", template, "--network", "bridge", "--", "/bin/true").wait(t)
	if code != 125 || !strings.Contains(stderr, "mdocker0") {
		t.Fatalf("bridge conflict: code=%d stderr=%q", code, stderr)
	}
	// Runtime rollback must leave this unrelated interface intact.
	networkCommand(t, "ip", "link", "show", "mdocker0")
	networkCommand(t, "ip", "link", "delete", "mdocker0")
	assertNetworkSnapshot(t, before)
	name := fmt.Sprintf("mc%d", sequence.Add(1))
	networkCommand(t, "ip", "link", "add", name, "type", "dummy")
	t.Cleanup(func() { _ = exec.Command("ip", "link", "delete", name).Run() })
	networkCommand(t, "ip", "address", "add", "10.231.0.99/24", "dev", name)
	networkCommand(t, "ip", "link", "set", name, "up")
	code, _, stderr = start(t, "", "run", "--rootfs", template, "--network", "bridge", "--", "/bin/true").wait(t)
	if code != 125 || !strings.Contains(stderr, "overlaps") {
		t.Fatalf("subnet conflict: code=%d stderr=%q", code, stderr)
	}
	networkCommand(t, "ip", "address", "show", "dev", name)
	assertNetworkSnapshot(t, before)
}

func TestNetworkPreviousBootJournalPreservesHost(t *testing.T) {
	require(t)
	before := networkSnapshot(t)
	shared := "/var/lib/mini-docker/runs/.network-shared.json"
	if _, err := os.Lstat(shared); !os.IsNotExist(err) {
		t.Fatalf("fixture requires no active bridge: %v", err)
	}
	path, err := os.MkdirTemp("/var/lib/mini-docker/runs", "run-oldboot-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(path); _ = os.Remove(shared) })
	previousBoot := "00000000-0000-0000-0000-000000000000"
	allocation := fmt.Sprintf(`{"boot_id":%q,"address":"10.231.0.2","publish":[]}`, previousBoot)
	if err := os.WriteFile(filepath.Join(path, "network.json"), []byte(allocation), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shared, []byte(fmt.Sprintf(`{"boot_id":%q,"forwarding":"0"}`, previousBoot)), 0600); err != nil {
		t.Fatal(err)
	}
	// A later boot can have unrelated interfaces, tables, and forwarding settings.
	networkCommand(t, "ip", "link", "add", "mdocker0", "type", "dummy")
	t.Cleanup(func() { _ = exec.Command("ip", "link", "delete", "mdocker0").Run() })
	sum := sha256.Sum256([]byte(filepath.Base(path)))
	table := fmt.Sprintf("mdocker_%x", sum[:6])
	networkCommand(t, "nft", "add", "table", "ip", table)
	t.Cleanup(func() { _ = exec.Command("nft", "delete", "table", "ip", table).Run() })
	forwarding := "/proc/sys/net/ipv4/ip_forward"
	prior, err := os.ReadFile(forwarding)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.WriteFile(forwarding, prior, 0600) })
	if err := os.WriteFile(forwarding, []byte("1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := network.Cleanup(path); err != nil {
		t.Fatal(err)
	}
	networkCommand(t, "ip", "link", "show", "mdocker0")
	networkCommand(t, "nft", "list", "table", "ip", table)
	value, err := os.ReadFile(forwarding)
	if err != nil || strings.TrimSpace(string(value)) != "1" {
		t.Fatalf("previous-boot recovery changed forwarding: %s %v", value, err)
	}
	for _, file := range []string{shared, filepath.Join(path, "network.json")} {
		if _, err := os.Lstat(file); !os.IsNotExist(err) {
			t.Fatalf("stale journal retained: %s %v", file, err)
		}
	}
	networkCommand(t, "ip", "link", "delete", "mdocker0")
	networkCommand(t, "nft", "delete", "table", "ip", table)
	if err := os.WriteFile(forwarding, prior, 0600); err != nil {
		t.Fatal(err)
	}
	assertNetworkSnapshot(t, before)
}

func TestNetworkPartialSetupRetainsReservations(t *testing.T) {
	require(t)
	before := networkSnapshot(t)
	child := exec.Command("unshare", "--net", "/bin/sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	own, err := os.Stat("/proc/self/ns/net")
	if err != nil {
		t.Fatal(err)
	}
	target := fmt.Sprintf("/proc/%d/ns/net", child.Process.Pid)
	var namespace *os.File
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		candidate, err := os.Open(target)
		if err == nil {
			info, err := candidate.Stat()
			if err == nil && !os.SameFile(own, info) {
				namespace = candidate
				break
			}
			candidate.Close()
		}
		time.Sleep(10 * time.Millisecond)
	}
	if namespace == nil {
		t.Fatal("unshare did not create a separate network namespace")
	}
	defer namespace.Close()
	// A conflicting interface inside the private namespace forces failure after
	// the host veth, bridge, and address reservation have already been created.
	poison := exec.Command("nsenter", "--net=/proc/self/fd/3", "--", "ip", "link", "add", "eth0", "type", "dummy")
	poison.ExtraFiles = []*os.File{namespace}
	if out, err := poison.CombinedOutput(); err != nil {
		t.Fatalf("poison private namespace: %v %s", err, out)
	}
	path, err := os.MkdirTemp("/var/lib/mini-docker/runs", "run-partial-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = network.Cleanup(path); _ = os.RemoveAll(path) })
	port := freeNetworkPort(t, "tcp")
	mapping, err := config.ParsePortMapping(fmt.Sprintf("127.0.0.1:%d:8080", port))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	lease, err := network.Setup(ctx, path, namespace, []config.PortMapping{mapping})
	if lease != nil {
		defer lease.Close()
	}
	if err == nil || lease == nil {
		t.Fatalf("partial failure must retain lease: lease=%v error=%v", lease, err)
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	if socket, err := net.Listen("tcp4", addr); err == nil {
		socket.Close()
		t.Fatal("partial startup released port before rollback")
	}
	if err := network.Cleanup(path); err != nil {
		t.Fatal(err)
	}
	lease.Close()
	socket, err := net.Listen("tcp4", addr)
	if err != nil {
		t.Fatalf("rollback retained port: %v", err)
	}
	socket.Close()
	assertNetworkSnapshot(t, before)
}
