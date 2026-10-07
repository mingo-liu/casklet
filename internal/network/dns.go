package network

import (
	"bufio"
	"io"
	"net/netip"
	"strings"
)

// ParseResolvers excludes local stubs that cannot be reached from a container.
func ParseResolvers(input io.Reader) []string {
	var servers []string
	seen := map[string]bool{}
	scanner := bufio.NewScanner(io.LimitReader(input, 65536))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 || fields[0] != "nameserver" {
			continue
		}
		ip, err := netip.ParseAddr(fields[1])
		if err != nil || !ip.Is4() || ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() || ip == netip.MustParseAddr("255.255.255.255") {
			continue
		}
		server := ip.String()
		if !seen[server] {
			servers = append(servers, server)
			seen[server] = true
		}
		if len(servers) == 3 {
			break
		}
	}
	return servers
}
