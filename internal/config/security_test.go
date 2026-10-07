package config

import "testing"

func TestSecurityMappings(t *testing.T) {
	base := Config{UserNS: true, UIDMappings: []IDMapping{{0, 200000, 65536}}, GIDMappings: []IDMapping{{0, 300000, 65536}}}
	if err := base.ValidateSecurity(); err != nil {
		t.Fatal(err)
	}
	if host, ok := MappedID(1234, base.UIDMappings); !ok || host != 201234 {
		t.Fatalf("mapped UID = %d, %v", host, ok)
	}
	for _, mappings := range [][]IDMapping{nil, {{0, 1, 0}}, {{1, 1, 1}}, {{0, 4294967294, 2}}, {{0, 2, 5}, {4, 20, 3}}, {{0, 2, 5}, {10, 4, 3}}} {
		cfg := base
		cfg.UIDMappings = mappings
		if err := cfg.ValidateSecurity(); err == nil {
			t.Fatalf("accepted invalid mappings %+v", mappings)
		}
	}
	base.User = &User{UID: 65536}
	if err := base.ValidateSecurity(); err == nil {
		t.Fatal("accepted unmapped user")
	}
	if err := (Config{UIDMappings: []IDMapping{{0, 0, 1}}}).ValidateSecurity(); err == nil {
		t.Fatal("accepted maps without namespace")
	}
	for _, value := range []string{"", "0:1", "0:1:2:3", "-1:1:2", "0:1:4294967296", "0: 1:2", "+0:1:2"} {
		if _, err := ParseIDMapping(value); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
	if m, err := ParseIDMapping("0:200000:65536"); err != nil || m != (IDMapping{0, 200000, 65536}) {
		t.Fatalf("parse = %+v, %v", m, err)
	}
}

func TestRootlessRestrictions(t *testing.T) {
	base := Config{Rootless: true, UserNS: true, UIDMappings: []IDMapping{{0, 1000, 1}}, GIDMappings: []IDMapping{{0, 1000, 1}}}
	if err := base.ValidateSecurity(); err != nil {
		t.Fatal(err)
	}
	for _, modify := range []func(*Config){
		func(c *Config) { c.Network = "bridge" },
		func(c *Config) { c.Image = "image" },
		func(c *Config) { c.User = &User{UID: 1} },
		func(c *Config) { c.UIDMappings = []IDMapping{{0, 1000, 2}} },
		func(c *Config) { c.Mounts = []BindMount{{Source: "/run/user/1000", Target: "/data"}} },
	} {
		cfg := base
		modify(&cfg)
		if err := cfg.ValidateSecurity(); err == nil {
			t.Fatalf("accepted %+v", cfg)
		}
	}
	if (Config{}).SeccompProfile() != "default" {
		t.Fatal("legacy configurations must receive filtering")
	}
	if err := (Config{Seccomp: "unknown"}).ValidateSecurity(); err == nil {
		t.Fatal("accepted unknown profile")
	}
}
