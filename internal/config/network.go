package config

import (
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
)

// PortMapping publishes an IPv4 host port to a container port.
type PortMapping struct {
	HostIP        string `json:"host_ip"`
	HostPort      uint16 `json:"host_port"`
	ContainerPort uint16 `json:"container_port"`
	Protocol      string `json:"protocol"`
}

// ParsePortMapping accepts [HOST_IP:]HOST_PORT:CONTAINER_PORT[/tcp|udp].
func ParsePortMapping(value string) (PortMapping, error) {
	p := PortMapping{HostIP: "0.0.0.0", Protocol: "tcp"}
	ports, protocol, found := strings.Cut(value, "/")
	if found {
		p.Protocol = protocol
	}
	parts := strings.Split(ports, ":")
	if len(parts) == 3 {
		p.HostIP = parts[0]
		parts = parts[1:]
	}
	if len(parts) != 2 {
		return p, errors.New("publish requires [HOST_IP:]HOST_PORT:CONTAINER_PORT[/tcp|udp]")
	}
	host, err := strconv.ParseUint(parts[0], 10, 16)
	if err != nil || host == 0 {
		return p, errors.New("host port must be between 1 and 65535")
	}
	target, err := strconv.ParseUint(parts[1], 10, 16)
	if err != nil || target == 0 {
		return p, errors.New("container port must be between 1 and 65535")
	}
	p.HostPort, p.ContainerPort = uint16(host), uint16(target)
	return p, ValidateNetwork("bridge", nil, []PortMapping{p}, nil)
}

func PortsConflict(a, b PortMapping) bool {
	return a.Protocol == b.Protocol && a.HostPort == b.HostPort &&
		(a.HostIP == b.HostIP || a.HostIP == "0.0.0.0" || b.HostIP == "0.0.0.0")
}

func ValidateNetwork(mode string, dns []string, ports []PortMapping, mounts []BindMount) error {
	if IsNamedNetwork(mode) {
		if err := ValidateNetworkName(mode); err != nil {
			return err
		}
	}
	if (mode == "" || mode == "none") && (len(dns) != 0 || len(ports) != 0) {
		return errors.New("DNS and published ports require bridge or named networking")
	}
	if len(dns) > 3 {
		return errors.New("at most three DNS servers are supported")
	}
	for _, server := range dns {
		ip, err := netip.ParseAddr(server)
		if err != nil || !ip.Is4() || ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() || ip == netip.MustParseAddr("255.255.255.255") {
			return fmt.Errorf("DNS server must be a non-loopback unicast IPv4 address: %q", server)
		}
	}
	if len(ports) > 32 {
		return errors.New("at most 32 published ports are supported")
	}
	for i, p := range ports {
		ip, err := netip.ParseAddr(p.HostIP)
		if err != nil || !ip.Is4() || ip.IsMulticast() || ip == netip.MustParseAddr("255.255.255.255") || p.HostPort == 0 || p.ContainerPort == 0 || (p.Protocol != "tcp" && p.Protocol != "udp") {
			return errors.New("published ports require an IPv4 host address, nonzero ports, and tcp or udp")
		}
		for _, previous := range ports[:i] {
			if PortsConflict(p, previous) {
				return errors.New("published host ports overlap")
			}
		}
	}
	if mode != "" && mode != "none" {
		for _, mount := range mounts {
			if pathsOverlap(mount.Target, "/etc/resolv.conf") {
				return errors.New("networking reserves /etc/resolv.conf; bind mounts must not cover it")
			}
		}
	}
	return nil
}

func (c Config) NetworkMode() string {
	if c.Network == "" {
		return "none"
	}
	return c.Network
}

var networkNamePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

func IsNamedNetwork(mode string) bool { return mode != "" && mode != "none" && mode != "bridge" }
func ValidateDNSName(name string) error {
	if !networkNamePattern.MatchString(name) {
		return errors.New("network names and aliases require a lowercase DNS label (1-63 letters, digits, or hyphens)")
	}
	return nil
}
func ValidateNetworkName(name string) error {
	if !IsNamedNetwork(name) {
		return errors.New("network name cannot be none or bridge")
	}
	return ValidateDNSName(name)
}
func (c Config) ValidateNetworkAliases() error {
	if len(c.NetworkAliases) > 32 {
		return errors.New("at most 32 network aliases are supported")
	}
	if len(c.NetworkAliases) > 0 && !IsNamedNetwork(c.Network) {
		return errors.New("network aliases require a named network")
	}
	seen := map[string]bool{}
	for _, name := range c.NetworkAliases {
		if err := ValidateDNSName(name); err != nil {
			return err
		}
		if seen[name] {
			return errors.New("duplicate network alias")
		}
		seen[name] = true
	}
	return nil
}
