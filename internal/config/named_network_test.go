package config

import (
	"strings"
	"testing"
)

func TestNamedNetworkNamesAndAliases(t *testing.T) {
	for _, name := range []string{"demo", "demo-2", "host", strings.Repeat("a", 63)} {
		if err := ValidateNetworkName(name); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	for _, name := range []string{"none", "bridge", "UPPER", "bad_name", "a.b", "-a", "a-", "../a", strings.Repeat("a", 64)} {
		if ValidateNetworkName(name) == nil {
			t.Fatalf("accepted %q", name)
		}
	}
	c := Config{Network: "demo", NetworkAliases: []string{"redis"}}
	if err := c.ValidateNetworkAliases(); err != nil {
		t.Fatal(err)
	}
	c.NetworkAliases = append(c.NetworkAliases, "redis")
	if c.ValidateNetworkAliases() == nil {
		t.Fatal("duplicate alias accepted")
	}
	c.Network = "bridge"
	if c.ValidateNetworkAliases() == nil {
		t.Fatal("legacy network aliases accepted")
	}
	p, _ := ParsePortMapping("8080:80")
	if err := ValidateNetwork("demo", []string{"8.8.8.8"}, []PortMapping{p}, nil); err != nil {
		t.Fatal(err)
	}
	if ValidateNetwork("demo", nil, nil, []BindMount{{Target: "/etc"}}) == nil {
		t.Fatal("named DNS mount accepted")
	}
}
