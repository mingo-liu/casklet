//go:build linux

package network

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestLinkOwnership(t *testing.T) {
	if os.Getenv("MINI_DOCKER_NETWORK_TEST") != "1" {
		t.Skip("requires root in the dedicated Linux VM")
	}
	name := fmt.Sprintf("mdt%d", os.Getpid())
	ctx := context.Background()
	if err := addLink(name, "mini-docker-test", "bridge", ""); err != nil {
		t.Fatal(err)
	}
	defer command(ctx, "ip", "", "link", "delete", name)
	for _, delay := range []time.Duration{0, 500 * time.Millisecond} {
		time.Sleep(delay)
		out, err := command(ctx, "ip", "", "-N", "-j", "-d", "link", "show", name)
		var device []struct {
			Group uint32 `json:"group,string"`
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(out, &device); err != nil || len(device) != 1 || device[0].Group != ownershipGroup("mini-docker-test") {
			t.Fatalf("lost ownership: %s %v", out, err)
		}
	}
}
