// Package remote relays macOS client signals to a guest CLI session.
package remote

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"github.com/mingo-liu/casklet/internal/config"
)

// PortCheckLimit bounds each private host port authorization message.
const PortCheckLimit = 16 << 10

// PortCheckRequest is a private guest-to-host startup handshake. It exposes
// only published ports, never the container's environment or private paths.
type PortCheckRequest struct {
	Version int                  `json:"version"`
	Kind    string               `json:"kind"`
	Publish []config.PortMapping `json:"publish"`
}

// PortCheckResult reports host authorization for a retained container start.
type PortCheckResult struct {
	Version int    `json:"version"`
	Allowed bool   `json:"allowed"`
	Error   string `json:"error,omitempty"`
}

// CheckHostPorts runs while the guest holds the lifecycle operation lock,
// after stopping the old execution and before publishing a new generation.
// Standard pipe/socket inputs close on cancellation; other readers own their read lifetime.
func CheckHostPorts(ctx context.Context, input io.Reader, output io.Writer, ports []config.PortMapping) error {
	if len(ports) == 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := json.NewEncoder(output).Encode(PortCheckRequest{Version: 1, Kind: "port-check", Publish: ports}); err != nil {
		return fmt.Errorf("request host port check: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	completed := make(chan error, 1)
	go func() { completed <- readPortCheck(input) }()
	select {
	case <-ctx.Done():
		if closer, ok := input.(io.Closer); ok {
			switch input.(type) {
			case net.Conn, *os.File, *io.PipeReader:
				_ = closer.Close()
				//lint:ignore S1005 Cancellation takes precedence over the completed handshake error.
				_ = <-completed
			}
		}
		return ctx.Err()
	case err := <-completed:
		return err
	}
}

// DecodePortCheck requires one bounded message with no unknown or trailing fields.
func DecodePortCheck(data []byte, target any) error {
	if len(data) > PortCheckLimit {
		return errors.New("host port check exceeds its size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("trailing host port check data")
	}
	return nil
}

func readPortCheck(input io.Reader) error {
	line, err := bufio.NewReader(io.LimitReader(input, PortCheckLimit+1)).ReadBytes('\n')
	if len(line) > PortCheckLimit {
		return errors.New("host port check exceeds its size limit")
	}
	if err != nil {
		return fmt.Errorf("read host port check: %w", err)
	}
	var result PortCheckResult
	if err := DecodePortCheck(line, &result); err != nil {
		return fmt.Errorf("decode host port check: %w", err)
	}
	if result.Version != 1 || result.Allowed && result.Error != "" {
		return errors.New("invalid host port check response")
	}
	if result.Allowed {
		return nil
	}
	if result.Error == "" {
		result.Error = "macOS host ports are unavailable"
	}
	return errors.New(result.Error)
}
