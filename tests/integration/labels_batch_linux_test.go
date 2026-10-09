//go:build linux

package integration

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/mingo-liu/casklet/internal/container"
	"golang.org/x/sys/unix"
)

func TestLabelsFilteringAndBatchLifecycle(t *testing.T) {
	require(t)
	project := backgroundName(t)
	selector := "label=project=" + project
	first := startBackground(t, backgroundName(t), []string{"--label", "project=" + project, "--label", "role=config", "--label", "role=api", "--label", "empty="}, "/bin/sleep", "300")
	second := startBackground(t, backgroundName(t), []string{"--label", "project=" + project, "--label", "role=db"}, "/bin/sleep", "300")
	outside := startBackground(t, backgroundName(t), []string{"--label", "project=" + project + "-outside"}, "/bin/sleep", "300")
	var inspection container.Inspection
	if err := json.Unmarshal([]byte(backgroundSuccess(t, "inspect", first)), &inspection); err != nil || inspection.Labels["project"] != project || inspection.Labels["role"] != "api" {
		t.Fatalf("inspection labels %v %v", inspection.Labels, err)
	}
	backgroundSuccess(t, "exec", first, "--", "/bin/sh", "-c", `test -z "${project+x}" && test -z "${role+x}"`)
	filtered := func(filters ...string) []container.Record {
		t.Helper()
		args := []string{"ps", "-a", "--json", "--filter", selector}
		for _, filter := range filters {
			args = append(args, "--filter", filter)
		}
		var records []container.Record
		if err := json.Unmarshal([]byte(backgroundSuccess(t, args...)), &records); err != nil {
			t.Fatal(err)
		}
		return records
	}
	if records := filtered("status=running", "health=none"); len(records) != 2 {
		t.Fatalf("scoped selection %v", records)
	}
	if records := filtered("label=empty="); len(records) != 1 || records[0].ID != first {
		t.Fatalf("empty label %v", records)
	}
	if code, out, diagnostic := backgroundCLI(t, "rm", "--filter", selector); code != 125 || out != "" || !strings.Contains(diagnostic, first) || !strings.Contains(diagnostic, second) {
		t.Fatalf("running batch remove %d %q %q", code, out, diagnostic)
	}
	backgroundSuccess(t, "stop", first)
	if code, out, diagnostic := backgroundCLI(t, "rm", first, second); code != 125 || out != first+"\n" || !strings.Contains(diagnostic, second) {
		t.Fatalf("partial removal %d %q %q", code, out, diagnostic)
	}
	backgroundSuccess(t, "stop", "--timeout", "0s", "--filter", selector)
	backgroundSuccess(t, "start", second)
	if err := json.Unmarshal([]byte(backgroundSuccess(t, "inspect", second)), &inspection); err != nil || inspection.Labels["project"] != project {
		t.Fatalf("restart labels %v %v", inspection.Labels, err)
	}
	third := startBackground(t, backgroundName(t), []string{"--label", "project=" + project}, "/bin/sleep", "300")
	if out := backgroundSuccess(t, "stop", "--timeout", "0s", third, second, third[:12]); out != third+"\n"+second+"\n" {
		t.Fatalf("operand order and deduplication: %q", out)
	}
	backgroundSuccess(t, "start", second)
	backgroundSuccess(t, "start", third)
	want := []string{second, third}
	sort.Strings(want)
	if out := backgroundSuccess(t, "stop", "--all", "--filter", selector); out != strings.Join(want, "\n")+"\n" {
		t.Fatalf("sorted stop %q", out)
	}
	if records := filtered("status=exited"); len(records) != 2 {
		t.Fatalf("stopped records %v", records)
	}
	if out := backgroundSuccess(t, "rm", "--filter", selector, "--filter", "status=exited"); out != strings.Join(want, "\n")+"\n" {
		t.Fatalf("sorted rm %q", out)
	}
	if out := backgroundSuccess(t, "rm", "--filter", selector); out != "" {
		t.Fatalf("empty selection %q", out)
	}
	if got := inspectBackground(t, outside); got.State != container.StateRunning {
		t.Fatalf("unselected record changed %+v", got)
	}
}

func TestBatchRemovalPinsFullIDsAcrossNameReuseAndOperationWait(t *testing.T) {
	require(t)
	first := startBackground(t, backgroundName(t), nil, "/bin/sleep", "300")
	reusedName := backgroundName(t)
	old := startBackground(t, reusedName, nil, "/bin/sleep", "300")
	backgroundSuccess(t, "stop", first, old)
	operation, err := os.OpenFile(filepath.Join("/var/lib/casklet/containers", old, ".operation"), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer operation.Close()
	if err := unix.Flock(int(operation.Fd()), unix.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "rm", first, reusedName)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var diagnostic bytes.Buffer
	cmd.Stderr = &diagnostic
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	line := make(chan string, 1)
	go func() { value, _ := bufio.NewReader(stdout).ReadString('\n'); line <- value }()
	select {
	case value := <-line:
		if value != first+"\n" {
			t.Fatalf("first removal %q", value)
		}
	case <-ctx.Done():
		t.Fatal("batch never reached the held operation lock")
	}
	// Model deletion after selection but before lock acquisition. Low-level store
	// removal intentionally bypasses the lifecycle operation lock to make the race
	// deterministic; production commands still recheck the pinned ID under it.
	store, err := container.OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Remove(ctx, old); err != nil {
		t.Fatal(err)
	}
	replacement := startBackground(t, reusedName, nil, "/bin/sleep", "300")
	if err := unix.Flock(int(operation.Fd()), unix.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	err = cmd.Wait()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 125 || !strings.Contains(diagnostic.String(), old) {
		t.Fatalf("pinned deletion: %v %q", err, diagnostic.String())
	}
	if got := inspectBackground(t, replacement); got.State != container.StateRunning {
		t.Fatalf("batch changed reused name %+v", got)
	}
}
