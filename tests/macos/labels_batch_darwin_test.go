//go:build darwin

package macos

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/mingo-liu/casklet/internal/container"
)

func TestLabelsFilteringAndBatchLifecycleThroughMacTransport(t *testing.T) {
	client(t)
	project := fmt.Sprintf("labels-%d", time.Now().UnixNano())
	selector := "label=project=" + project
	directory := hostDirectory(t)
	filename := filepath.Join(directory, "run.json")
	body := fmt.Sprintf(`{"detach":true,"label":["project=%s","role=config"],"command":["/bin/sleep","300"]}`, project)
	if err := os.WriteFile(filename, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	first := strings.TrimSpace(success(t, "run", "--config", filename, "--label", "role=api", "--label", "empty="))
	t.Cleanup(func() { command(t, "stop", "--timeout", "0s", first); command(t, "rm", first) })
	second := detached(t, "--label", "project="+project, "--label", "role=db", "--", "/bin/sleep", "300")
	outside := detached(t, "--label", "project="+project+"-outside", "--", "/bin/sleep", "300")
	var inspection container.Inspection
	if err := json.Unmarshal([]byte(success(t, "inspect", first)), &inspection); err != nil {
		t.Fatal(err)
	}
	if inspection.Labels["project"] != project || inspection.Labels["role"] != "api" {
		t.Fatalf("labels %v", inspection.Labels)
	}
	if out := success(t, "exec", first, "--", "/bin/sh", "-c", `test -z "${project+x}" && test -z "${role+x}"`); out != "" {
		t.Fatal(out)
	}
	filtered := func(filters ...string) []container.Record {
		t.Helper()
		args := []string{"ps", "-a", "--json", "--filter", selector}
		for _, filter := range filters {
			args = append(args, "--filter", filter)
		}
		var records []container.Record
		if err := json.Unmarshal([]byte(success(t, args...)), &records); err != nil {
			t.Fatal(err)
		}
		return records
	}
	if records := filtered("status=running", "health=none"); len(records) != 2 {
		t.Fatalf("scoped records %v", records)
	}
	if records := filtered("label=empty="); len(records) != 1 || records[0].ID != first {
		t.Fatalf("empty label %v", records)
	}
	if out, diagnostic, code := command(t, "rm", "--filter", selector); code != 125 || out != "" || !strings.Contains(diagnostic, first) || !strings.Contains(diagnostic, second) {
		t.Fatalf("running batch remove %d %q %q", code, out, diagnostic)
	}
	success(t, "stop", first)
	if out, diagnostic, code := command(t, "rm", first, second); code != 125 || out != first+"\n" || !strings.Contains(diagnostic, second) {
		t.Fatalf("partial removal %d %q %q", code, out, diagnostic)
	}
	success(t, "stop", "--timeout", "0s", "--filter", selector)
	success(t, "start", second)
	if err := json.Unmarshal([]byte(success(t, "inspect", second)), &inspection); err != nil || inspection.Labels["project"] != project {
		t.Fatalf("restart lost labels %v %v", inspection.Labels, err)
	}
	third := detached(t, "--label", "project="+project, "--", "/bin/sleep", "300")
	if out := success(t, "stop", "--timeout", "0s", third, second, third[:12]); out != third+"\n"+second+"\n" {
		t.Fatalf("operand order and deduplication: %q", out)
	}
	success(t, "start", second)
	success(t, "start", third)
	want := []string{second, third}
	sort.Strings(want)
	if out := success(t, "stop", "--all", "--filter", selector); out != strings.Join(want, "\n")+"\n" {
		t.Fatalf("deterministic stop %q", out)
	}
	if records := filtered("status=exited"); len(records) != 2 {
		t.Fatalf("stopped records %v", records)
	}
	if out := success(t, "rm", "--filter", selector, "--filter", "status=exited"); out != strings.Join(want, "\n")+"\n" {
		t.Fatalf("deterministic rm %q", out)
	}
	if records := filtered(); !reflect.DeepEqual(records, []container.Record{}) {
		t.Fatalf("removed selection %v", records)
	}
	if out := success(t, "rm", "--filter", selector); out != "" {
		t.Fatalf("empty selection %q", out)
	}
	if got := inspectHost(t, outside); got.State != container.StateRunning {
		t.Fatalf("unselected container changed %+v", got)
	}
}
