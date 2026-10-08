//go:build darwin

package macos

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestVolumeArchiveRoundTripOverMacTransport(t *testing.T) {
	name := fmt.Sprintf("archive-%d", time.Now().UnixNano())
	copyName := name + "-copy"
	success(t, "volume", "create", name)
	t.Cleanup(func() { command(t, "volume", "rm", name); command(t, "volume", "rm", copyName) })
	mount := "type=volume,source=" + name + ",target=/data"
	id := detached(t, "--mount", mount, "--", "/bin/sh", "-c", "printf 'exact\\000bytes' > /data/value; chmod 640 /data/value; ln /data/value /data/hard; ln -s value /data/sym; echo ready; sleep 300")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if strings.Contains(success(t, "logs", id), "ready") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("writer not ready")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, _, code := command(t, "volume", "export", name); code != 125 {
		t.Fatal("exported active volume")
	}
	success(t, "stop", id)
	archive := []byte(success(t, "volume", "export", name))
	reader := tar.NewReader(bytes.NewReader(archive))
	if _, err := reader.Next(); err != nil {
		t.Fatalf("stdout is not tar: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, client(t), "volume", "restore", copyName)
	cmd.Stdin = bytes.NewReader(archive)
	output, err := cmd.CombinedOutput()
	if err != nil || string(output) != copyName+"\n" {
		t.Fatalf("restore: %q %v", output, err)
	}
	if out := success(t, "run", "--mount", "type=volume,source="+copyName+",target=/data,readonly", "--", "/bin/sh", "-c", "cat /data/sym; test /data/value -ef /data/hard; stat -c %a /data/value"); out != "exact\x00bytes640\n" {
		t.Fatalf("round trip: %q", out)
	}
}
