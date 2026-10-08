package machine

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"

	"github.com/mingo-liu/casklet/internal/config"
	"github.com/mingo-liu/casklet/internal/remote"
)

func probePort(mapping config.PortMapping) error {
	addresses := []string{mapping.HostIP}
	if mapping.HostIP == "0.0.0.0" {
		// BSD may allow a wildcard TCP listener alongside an existing listener
		// on a specific address. That listener would shadow Lima's forwarding.
		interfaces, err := net.InterfaceAddrs()
		if err != nil {
			return err
		}
		seen := map[string]bool{"0.0.0.0": true}
		for _, address := range interfaces {
			ip, _, err := net.ParseCIDR(address.String())
			if err != nil || ip.To4() == nil || seen[ip.String()] {
				continue
			}
			seen[ip.String()] = true
			addresses = append(addresses, ip.String())
		}
	}
	for _, address := range addresses {
		if err := probePortAddress(mapping, address); err != nil {
			return err
		}
	}
	return nil
}

func probePortAddress(mapping config.PortMapping, hostIP string) error {
	address := net.JoinHostPort(hostIP, strconv.Itoa(int(mapping.HostPort)))
	if mapping.Protocol == "udp" {
		listener, err := net.ListenPacket("udp4", address)
		if err != nil {
			return err
		}
		return listener.Close()
	}
	listener, err := net.Listen("tcp4", address)
	if err != nil {
		return err
	}
	return listener.Close()
}

func hostPortMappings(ports []config.PortMapping) ([]config.PortMapping, error) {
	result := append([]config.PortMapping(nil), ports...)
	for i := range result {
		switch result[i].HostIP {
		case "127.0.0.2":
			result[i].HostIP = "0.0.0.0"
		case "127.0.0.3":
			result[i].HostIP = "127.0.0.1"
		}
		if err := validateMacPort(result[i]); err != nil {
			return nil, err
		}
	}
	if err := config.ValidateNetwork("bridge", nil, result, nil); err != nil {
		return nil, err
	}
	return result, nil
}

func waitHostPorts(ctx context.Context, ports []config.PortMapping, limit time.Duration) error {
	ports, err := hostPortMappings(ports)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	var conflict error
	for {
		if err := ctx.Err(); err != nil {
			if errors.Is(err, context.DeadlineExceeded) && conflict != nil {
				return fmt.Errorf("macOS host port unavailable: %w; stop the process using this port before retrying start or restart", conflict)
			}
			return err
		}
		conflict = nil
		for _, port := range ports {
			if err := probePort(port); err != nil {
				conflict = err
				break
			}
		}
		if conflict == nil {
			return nil
		}
		// Lima closes old forwarding sockets asynchronously after guest stop.
		// Probe every mapping again; never exempt an occupied port by identity.
		select {
		case <-ctx.Done():
		case <-ticker.C:
		}
	}
}

// The first guest output line may be a private port check. Subsequent output
// remains the normal container ID. Pipe EOF rejects malformed/disconnected
// checks while the guest still owns its operation lock and old generation.
type portCheckOutput struct {
	ctx      context.Context
	response io.WriteCloser
	stdout   io.Writer
	pending  []byte
	handled  bool
}

func (output *portCheckOutput) Write(data []byte) (int, error) {
	length := len(data)
	if output.handled {
		return output.stdout.Write(data)
	}
	output.pending = append(output.pending, data...)
	end := bytes.IndexByte(output.pending, '\n')
	if end < 0 {
		if len(output.pending) <= remote.PortCheckLimit {
			return length, nil
		}
		output.response.Close()
		return 0, errors.New("guest port check exceeds its size limit")
	}
	output.handled = true
	defer output.response.Close()
	line := output.pending[:end+1]
	remainder := output.pending[end+1:]
	if len(line) > remote.PortCheckLimit {
		return 0, errors.New("guest port check exceeds its size limit")
	}
	if bytes.HasPrefix(bytes.TrimSpace(line), []byte("{")) {
		var request remote.PortCheckRequest
		if err := remote.DecodePortCheck(line, &request); err != nil {
			return 0, fmt.Errorf("invalid guest port check: %w", err)
		}
		if request.Version != 1 || request.Kind != "port-check" {
			return 0, errors.New("invalid guest port check request")
		}
		err := waitHostPorts(output.ctx, request.Publish, 5*time.Second)
		result := remote.PortCheckResult{Version: 1, Allowed: err == nil}
		if err != nil {
			result.Error = err.Error()
		}
		if err := json.NewEncoder(output.response).Encode(result); err != nil {
			return 0, fmt.Errorf("authorize guest ports: %w", err)
		}
	} else {
		// Idempotent start and containers without published ports need no check.
		id, err := hex.DecodeString(string(bytes.TrimSpace(line)))
		if err != nil || len(id) != 16 {
			return 0, errors.New("invalid guest lifecycle result")
		}
		if n, err := output.stdout.Write(line); err != nil {
			return 0, err
		} else if n != len(line) {
			return 0, io.ErrShortWrite
		}
	}
	if n, err := output.stdout.Write(remainder); err != nil {
		return 0, err
	} else if n != len(remainder) {
		return 0, io.ErrShortWrite
	}
	output.pending = nil
	return length, nil
}
