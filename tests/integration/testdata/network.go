package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"
)

// Bounded deterministic fixture for HTTP, UDP, and DNS in real namespaces.
func networkServer(withDNS bool) {
	timer := time.AfterFunc(90*time.Second, func() { os.Exit(0) })
	defer timer.Stop()
	echo, err := net.ListenPacket("udp4", ":7777")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer echo.Close()
	go func() {
		buffer := make([]byte, 1024)
		for {
			n, addr, err := echo.ReadFrom(buffer)
			if err != nil {
				return
			}
			_, _ = echo.WriteTo(append([]byte("echo:"), buffer[:n]...), addr)
		}
	}()
	if withDNS {
		dns, err := net.ListenPacket("udp4", ":53")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		defer dns.Close()
		tcp, err := net.Listen("tcp4", ":53")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		defer tcp.Close()
		go func() {
			buffer := make([]byte, 1024)
			for {
				n, addr, err := dns.ReadFrom(buffer)
				if err != nil {
					return
				}
				if answer := fixtureDNSResponse(buffer[:n]); answer != nil {
					dns.WriteTo(answer, addr)
				}
			}
		}()
		slots := make(chan struct{}, 32)
		go func() {
			for {
				c, err := tcp.Accept()
				if err != nil {
					return
				}
				select {
				case slots <- struct{}{}:
				default:
					c.Close()
					continue
				}
				go func() {
					defer func() { <-slots }()
					defer c.Close()
					c.SetDeadline(time.Now().Add(3 * time.Second))
					var header [2]byte
					if _, err := io.ReadFull(c, header[:]); err != nil {
						return
					}
					n := int(binary.BigEndian.Uint16(header[:]))
					if n < 17 || n > 1024 {
						return
					}
					packet := make([]byte, n)
					if _, err := io.ReadFull(c, packet); err != nil {
						return
					}
					answer := fixtureDNSResponse(packet)
					if answer == nil {
						return
					}
					binary.BigEndian.PutUint16(header[:], uint16(len(answer)))
					c.Write(append(header[:], answer...))
				}()
			}
		}()

	}
	fmt.Println("network-server-ready")
	server := &http.Server{Addr: ":8080", ReadHeaderTimeout: 2 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprintf(w, "peer=%s network-ok\n", r.RemoteAddr) })}
	if err := server.ListenAndServe(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func networkClient() {
	transport := &http.Transport{DialContext: (&net.Dialer{Timeout: 3 * time.Second, Resolver: &net.Resolver{PreferGo: true}}).DialContext}
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	response, err := client.Get("http://fixture.test:8080/")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer response.Body.Close()
	if _, err := io.Copy(os.Stdout, response.Body); err != nil {
		os.Exit(1)
	}
}

func networkResolve() {
	if len(os.Args) != 4 || (os.Args[2] != "udp" && os.Args[2] != "tcp") {
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	resolver := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _ string, address string) (net.Conn, error) {
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, os.Args[2], address)
	}}
	ips, err := resolver.LookupHost(ctx, os.Args[3])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for _, ip := range ips {
		fmt.Println(ip)
	}
}

func fixtureDNSResponse(packet []byte) []byte {
	if len(packet) < 17 {
		return nil
	}
	end := 12
	for end < len(packet) && packet[end] != 0 {
		n := int(packet[end])
		if n > 63 {
			return nil
		}
		end += n + 1
	}
	end += 5
	if end > len(packet) {
		return nil
	}
	response := append([]byte(nil), packet[:end]...)
	binary.BigEndian.PutUint16(response[2:4], 0x8180)
	for i := 6; i < 12; i++ {
		response[i] = 0
	}
	if binary.BigEndian.Uint16(response[end-4:end-2]) == 1 {
		binary.BigEndian.PutUint16(response[6:8], 1)
		response = append(response, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 5, 0, 4, 198, 18, 0, 2)
	}
	return response
}
