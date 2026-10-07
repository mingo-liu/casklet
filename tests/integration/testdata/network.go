package main

import (
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
		go func() {
			buffer := make([]byte, 1024)
			for {
				n, addr, err := dns.ReadFrom(buffer)
				if err != nil {
					return
				}
				if n < 17 {
					continue
				}
				end := 12
				for end < n && buffer[end] != 0 {
					end += int(buffer[end]) + 1
				}
				end += 5
				if end > n {
					continue
				}
				response := append([]byte(nil), buffer[:end]...)
				binary.BigEndian.PutUint16(response[2:4], 0x8180)
				binary.BigEndian.PutUint16(response[6:8], 0)
				binary.BigEndian.PutUint16(response[8:10], 0)
				binary.BigEndian.PutUint16(response[10:12], 0)
				if binary.BigEndian.Uint16(response[end-4:end-2]) == 1 {
					binary.BigEndian.PutUint16(response[6:8], 1)
					response = append(response, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 5, 0, 4, 198, 18, 0, 2)
				}
				_, _ = dns.WriteTo(response, addr)
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
