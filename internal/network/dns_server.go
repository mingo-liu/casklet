package network

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"time"
)

// DNS sockets belong to one execution, not a shared daemon. Queries are bounded
// and answers are never cached, so a replacement execution is visible immediately.
type dnsServer struct {
	port     int
	udp      *net.UDPConn
	tcp      net.Listener
	wg       sync.WaitGroup
	slots    chan struct{}
	stop     chan struct{}
	source   string
	lookup   func(string) (net.IP, bool, error)
	upstream []string
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	conns    map[net.Conn]bool
	once     sync.Once
	quiesce  sync.Once
}

func newDNS(gateway, source string, upstream []string, lookup func(string) (net.IP, bool, error)) (*dnsServer, error) {
	tcp, err := net.Listen("tcp4", net.JoinHostPort(gateway, "0"))
	if err != nil {
		return nil, err
	}
	port := tcp.Addr().(*net.TCPAddr).Port
	udp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP(gateway), Port: port})
	if err != nil {
		tcp.Close()
		return nil, err
	}
	s := &dnsServer{port: port, udp: udp, tcp: tcp, slots: make(chan struct{}, 32), stop: make(chan struct{}), source: source, upstream: append([]string(nil), upstream...), lookup: lookup}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.conns = map[net.Conn]bool{}
	s.wg.Add(2)
	go s.serveUDP()
	go s.serveTCP()
	return s, nil
}

// Quiesce ends queries while keeping bound sockets reserved through NAT cleanup.
func (s *dnsServer) Quiesce() {
	s.quiesce.Do(func() {
		s.cancel()
		close(s.stop)
		s.udp.SetReadDeadline(time.Now())
		s.tcp.(*net.TCPListener).SetDeadline(time.Now())
		s.mu.Lock()
		for c := range s.conns {
			c.Close()
		}
		s.mu.Unlock()
		s.wg.Wait()
	})
}
func (s *dnsServer) Close() { s.once.Do(func() { s.Quiesce(); s.udp.Close(); s.tcp.Close() }) }
func (s *dnsServer) track(c net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx.Err() != nil {
		c.Close()
		return false
	}
	s.conns[c] = true
	return true
}
func (s *dnsServer) release(c net.Conn) { s.mu.Lock(); delete(s.conns, c); s.mu.Unlock(); c.Close() }
func (s *dnsServer) acquire() bool {
	select {
	case s.slots <- struct{}{}:
		return true
	default:
		return false
	}
}
func (s *dnsServer) allowed(ip net.IP) bool { return s.source == "" || ip.String() == s.source }
func (s *dnsServer) serveUDP() {
	defer s.wg.Done()
	buf := make([]byte, 4096)
	for {
		n, peer, err := s.udp.ReadFromUDP(buf)
		if err != nil {
			return
		}
		if !s.allowed(peer.IP) || !s.acquire() {
			continue
		}
		packet := append([]byte(nil), buf[:n]...)
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer func() { <-s.slots }()
			if answer := s.answer(packet, false); answer != nil {
				s.udp.WriteToUDP(answer, peer)
			}
		}()
	}
}
func (s *dnsServer) serveTCP() {
	defer s.wg.Done()
	for {
		c, err := s.tcp.Accept()
		if err != nil {
			return
		}
		if !s.allowed(c.RemoteAddr().(*net.TCPAddr).IP) || !s.acquire() {
			c.Close()
			continue
		}
		if !s.track(c) {
			<-s.slots
			continue
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer func() { <-s.slots }()
			defer s.release(c)
			c.SetDeadline(time.Now().Add(3 * time.Second))
			var frame [2]byte
			if _, err := io.ReadFull(c, frame[:]); err != nil {
				return
			}
			n := int(binary.BigEndian.Uint16(frame[:]))
			if n < 12 || n > 4096 {
				return
			}
			packet := make([]byte, n)
			if _, err := io.ReadFull(c, packet); err != nil {
				return
			}
			answer := s.answer(packet, true)
			if answer == nil {
				return
			}
			binary.BigEndian.PutUint16(frame[:], uint16(len(answer)))
			c.Write(append(frame[:], answer...))
		}()
	}
}
func question(packet []byte) (name string, typ uint16, end int, err error) {
	if len(packet) < 12 || len(packet) > 4096 || packet[2]&0xf8 != 0 || binary.BigEndian.Uint16(packet[4:6]) != 1 {
		return "", 0, 0, errors.New("invalid DNS question")
	}
	var labels []string
	i := 12
	size := 0
	for {
		if i >= len(packet) {
			return "", 0, 0, io.ErrUnexpectedEOF
		}
		n := int(packet[i])
		i++
		if n == 0 {
			break
		}
		if n > 63 || i+n > len(packet) {
			return "", 0, 0, errors.New("invalid DNS label")
		}
		size += n + 1
		if size > 253 {
			return "", 0, 0, errors.New("DNS name too long")
		}
		labels = append(labels, string(packet[i:i+n]))
		i += n
	}
	if i+4 > len(packet) || binary.BigEndian.Uint16(packet[i+2:i+4]) != 1 {
		return "", 0, 0, errors.New("unsupported DNS question")
	}
	return strings.ToLower(strings.Join(labels, ".")), binary.BigEndian.Uint16(packet[i : i+2]), i + 4, nil
}
func response(packet []byte, end int, rcode byte) []byte {
	out := append([]byte(nil), packet[:end]...)
	out[2] = 0x80 | packet[2]&1
	out[3] = 0x80 | rcode
	for i := 6; i < 12; i++ {
		out[i] = 0
	}
	return out
}
func (s *dnsServer) answer(packet []byte, tcp bool) []byte {
	name, typ, end, err := question(packet)
	if err != nil {
		return nil
	}
	ip, known, err := s.lookup(name)
	if err != nil {
		return response(packet, end, 2)
	}
	if known {
		out := response(packet, end, 0)
		out[2] |= 4
		if typ == 1 && ip.To4() != nil {
			binary.BigEndian.PutUint16(out[6:8], 1)
			out = append(out, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 0, 0, 4)
			out = append(out, ip.To4()...)
		}
		return out
	}
	// Single labels and .casklet names are network-local and must not leak upstream.
	if !strings.Contains(name, ".") || strings.HasSuffix(name, ".casklet") {
		return response(packet, end, 3)
	}
	for _, server := range s.upstream {
		select {
		case <-s.stop:
			return nil
		default:
		}
		protocol := "udp4"
		if tcp {
			protocol = "tcp4"
		}
		c, err := (&net.Dialer{Timeout: time.Second}).DialContext(s.ctx, protocol, net.JoinHostPort(server, "53"))
		if err != nil {
			continue
		}
		if !s.track(c) {
			return nil
		}
		c.SetDeadline(time.Now().Add(time.Second))
		var answer []byte
		if tcp {
			var header [2]byte
			binary.BigEndian.PutUint16(header[:], uint16(len(packet)))
			_, err = c.Write(append(header[:], packet...))
			if err == nil {
				_, err = io.ReadFull(c, header[:])
				if err == nil {
					n := int(binary.BigEndian.Uint16(header[:]))
					if n >= 12 && n <= 4096 {
						answer = make([]byte, n)
						_, err = io.ReadFull(c, answer)
					} else {
						err = errors.New("oversized upstream DNS answer")
					}
				}
			}
		} else {
			_, err = c.Write(packet)
			if err == nil {
				buf := make([]byte, 4096)
				var n int
				n, err = c.Read(buf)
				answer = buf[:n]
			}
		}
		s.release(c)
		if err == nil && len(answer) >= 12 && answer[2]&0x80 != 0 && answer[0] == packet[0] && answer[1] == packet[1] {
			return answer
		}
	}
	return response(packet, end, 2)
}
