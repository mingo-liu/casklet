//go:build darwin

package macos

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/mingo-liu/casklet/internal/container"
)

func TestStatsMultipleAndWatchThroughMacClient(t *testing.T) {
	binary := client(t)
	first := detached(t, "--", "/bin/sleep", "300")
	second := detached(t, "--", "/bin/sleep", "300")
	var rows []container.Statistics
	if err := json.Unmarshal([]byte(success(t, "stats", "--json", "--interval", "100ms", first, second)), &rows); err != nil || len(rows) != 2 || rows[0].ID != first || rows[1].ID != second || rows[0].MemoryBytes == nil || rows[1].CPUPercent == nil {
		t.Fatalf("multi stats: %v %v", rows, err)
	}
	cmd := exec.Command(binary, "stats", "--watch", "--json", "--interval", "100ms", first, second)
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	})
	frames := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(pipe)
		for i := 0; i < 2; i++ {
			if !scanner.Scan() {
				err := scanner.Err()
				if err == nil {
					err = errors.New("watch ended before two frames")
				}
				frames <- err
				return
			}
			var frame []container.Statistics
			err := json.Unmarshal(scanner.Bytes(), &frame)
			if err == nil && (len(frame) != 2 || frame[0].ID != first || frame[1].ID != second) {
				err = errors.New("invalid watch identities")
			}
			if err != nil {
				frames <- err
				return
			}
		}
		frames <- nil
	}()
	select {
	case err := <-frames:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("watch did not stream frames")
	}
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 130 {
			t.Fatalf("watch interrupt: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("watch did not cancel")
	}
	success(t, "exec", first, "--", "/bin/true")
}
