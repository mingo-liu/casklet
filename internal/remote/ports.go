package remote

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/mingo-liu/casklet/internal/config"
)

const PortCheckLimit = 16 << 10

// PortCheckRequest is a private guest-to-host startup handshake. It exposes
// only published ports, never the container's environment or private paths.
type PortCheckRequest struct {
	Version int                  `json:"version"`
	Kind    string               `json:"kind"`
	Publish []config.PortMapping `json:"publish"`
}

type PortCheckResult struct {
	Version int    `json:"version"`
	Allowed bool   `json:"allowed"`
	Error   string `json:"error,omitempty"`
}

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

// CheckHostPorts runs while the guest holds the lifecycle operation lock,
// after stopping the old execution and before publishing a new generation.
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
	go func() {
		line, err := bufio.NewReader(io.LimitReader(input, PortCheckLimit+1)).ReadBytes('\n')
		if len(line) > PortCheckLimit {
			completed <- errors.New("host port check exceeds its size limit")
			return
		}
		if err != nil {
			completed <- fmt.Errorf("read host port check: %w", err)
			return
		}
		var result PortCheckResult
		if err := DecodePortCheck(line, &result); err != nil {
			completed <- fmt.Errorf("decode host port check: %w", err)
		} else if result.Version != 1 || result.Allowed && result.Error != "" {
			completed <- errors.New("invalid host port check response")
		} else if !result.Allowed {
			if result.Error == "" {
				result.Error = "macOS host ports are unavailable"
			}
			completed <- errors.New(result.Error)
		} else {
			completed <- nil
		}
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-completed:
		return err
	}
}
