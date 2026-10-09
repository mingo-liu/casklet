package config

import "testing"

func TestNetworkValidation(t *testing.T) {
	for _, value := range []string{"8080:80", "127.0.0.1:5353:53/udp", "0.0.0.0:65535:1/tcp"} {
		if _, err := ParsePortMapping(value); err != nil {
			t.Errorf("%s: %v", value, err)
		}
	}
	for _, value := range []string{"80", "0:80", "80:0", "65536:80", "-1:80", "localhost:80:80", "[::1]:80:80", "80:80/sctp", "80:80/tcp/udp", ":80:80", "224.0.0.1:80:80", "255.255.255.255:80:80"} {
		if _, err := ParsePortMapping(value); err == nil {
			t.Errorf("accepted %q", value)
		}
	}
	for _, server := range []string{"127.0.0.53", "0.0.0.0", "::1", "8.8.8.8\noptions foo", "224.0.0.1", "255.255.255.255"} {
		if err := ValidateNetwork("bridge", []string{server}, nil, nil); err == nil {
			t.Errorf("accepted DNS %q", server)
		}
	}
	p, _ := ParsePortMapping("8080:80")
	for _, mode := range []string{"", "none", "bad/network", "UPPER"} {
		if err := ValidateNetwork(mode, nil, []PortMapping{p}, nil); err == nil {
			t.Errorf("accepted publish with mode %q", mode)
		}
	}
	tcp, _ := ParsePortMapping("127.0.0.1:8080:90")
	udp, _ := ParsePortMapping("8080:90/udp")
	other, _ := ParsePortMapping("127.0.0.2:8080:90")
	if err := ValidateNetwork("bridge", []string{"8.8.8.8"}, []PortMapping{tcp, udp, other}, nil); err != nil {
		t.Fatal(err)
	}
	if err := ValidateNetwork("bridge", nil, []PortMapping{p, tcp}, nil); err == nil {
		t.Fatal("accepted wildcard conflict")
	}
	for _, target := range []string{"/etc", "/etc/resolv.conf", "/etc/resolv.conf/sub"} {
		if err := ValidateNetwork("bridge", nil, nil, []BindMount{{Target: target}}); err == nil {
			t.Errorf("accepted DNS mount %s", target)
		}
	}
}
