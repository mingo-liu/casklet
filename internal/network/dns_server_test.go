package network

import (
	"encoding/binary"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

func query(name string, typ uint16) []byte {
	b := []byte{0x12, 0x34, 1, 0, 0, 1, 0, 0, 0, 0, 0, 0}
	for _, part := range strings.Split(name, ".") {
		b = append(b, byte(len(part)))
		b = append(b, part...)
	}
	return append(b, 0, byte(typ>>8), byte(typ), 0, 1)
}
func TestDNSUDPAndTCPRefreshAndMissingNames(t *testing.T) {
	var mu sync.Mutex
	ip := net.IPv4(10, 232, 0, 2)
	s, err := newDNS("127.0.0.1", "", nil, func(name string) (net.IP, bool, error) {
		mu.Lock()
		defer mu.Unlock()
		return append(net.IP(nil), ip...), name == "redis" || name == "redis.casklet", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, protocol := range []string{"udp", "tcp"} {
		c, err := net.Dial(protocol, s.tcp.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		c.SetDeadline(time.Now().Add(time.Second))
		q := query("redis", 1)
		if protocol == "tcp" {
			q = append([]byte{0, byte(len(q))}, q...)
		}
		if _, err = c.Write(q); err != nil {
			t.Fatal(err)
		}
		var answer []byte
		if protocol == "tcp" {
			var size [2]byte
			if _, err = io.ReadFull(c, size[:]); err != nil {
				t.Fatal(err)
			}
			answer = make([]byte, binary.BigEndian.Uint16(size[:]))
			_, err = io.ReadFull(c, answer)
		} else {
			buf := make([]byte, 4096)
			n, e := c.Read(buf)
			err = e
			answer = buf[:n]
		}
		c.Close()
		if err != nil || len(answer) < 4 || !net.IP(answer[len(answer)-4:]).Equal(ip) || answer[3]&15 != 0 {
			t.Fatalf("%s answer %x: %v", protocol, answer, err)
		}
	}
	mu.Lock()
	ip = net.IPv4(10, 232, 0, 3)
	mu.Unlock()
	answer := s.answer(query("redis.casklet", 1), false)
	if !net.IP(answer[len(answer)-4:]).Equal(ip) {
		t.Fatalf("stale answer: %x", answer)
	}
	for _, typ := range []uint16{28, 15} {
		a := s.answer(query("redis", typ), false)
		if a[3]&15 != 0 || binary.BigEndian.Uint16(a[6:8]) != 0 {
			t.Fatalf("known name wrong type: %x", a)
		}
	}
	if a := s.answer(query("missing", 1), false); a[3]&15 != 3 {
		t.Fatalf("missing should be NXDOMAIN: %x", a)
	}
	if a := s.answer(query("missing.casklet", 1), false); a[3]&15 != 3 {
		t.Fatalf("missing local suffix: %x", a)
	}
}
func TestDNSCloseCancelsIncompleteTCPAndBoundsWorkers(t *testing.T) {
	s, err := newDNS("127.0.0.1", "", nil, func(string) (net.IP, bool, error) { return nil, false, nil })
	if err != nil {
		t.Fatal(err)
	}
	conns := []net.Conn{}
	defer func() {
		for _, c := range conns {
			c.Close()
		}
	}()
	for i := 0; i < 32; i++ {
		c, err := net.Dial("tcp", s.tcp.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c)
		c.Write([]byte{0})
	}
	deadline := time.Now().Add(time.Second)
	for len(s.slots) < 32 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(s.slots) != 32 {
		t.Fatal("connections were not accepted")
	}
	c, err := net.Dial("tcp", s.tcp.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := c.Read(make([]byte, 1)); err == nil {
		t.Fatal("excess connection accepted")
	}
	start := time.Now()
	s.Close()
	s.Close()
	if time.Since(start) > time.Second {
		t.Fatal("DNS shutdown blocked on incomplete requests")
	}
}
func TestDNSRejectsMalformedPacketsAndWrongSources(t *testing.T) {
	s := &dnsServer{source: "10.232.0.2"}
	if s.allowed(net.ParseIP("10.232.0.3")) {
		t.Fatal("accepted another container")
	}
	cases := [][]byte{nil, make([]byte, 12), query("redis", 1)[:14], append(query("redis", 1), make([]byte, 4096)...)}
	compressed := query("redis", 1)
	compressed[12] = 0xc0
	cases = append(cases, compressed)
	for _, q := range cases {
		if _, _, _, err := question(q); err == nil {
			t.Fatalf("accepted malformed %x", q)
		}
	}
}
