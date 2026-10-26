package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/mingo-liu/casklet/internal/config"
	"github.com/mingo-liu/casklet/internal/container"
	"github.com/mingo-liu/casklet/internal/project"
)

func completeListingRecord() container.Record {
	at := time.Date(2026, 10, 26, 1, 2, 3, 0, time.UTC)
	code, grace := 7, 3*time.Second
	boot := "00000000-0000-0000-0000-000000000001"
	return container.Record{
		ExitReason: "oom_killed", OOMKilled: true,
		Labels:        map[string]string{project.LabelName: "demo", project.LabelService: "app", project.LabelSpec: "spec"},
		Health:        &container.Health{Status: container.HealthStopped, FailingStreak: 1, Checks: []container.HealthResult{{StartedAt: at, FinishedAt: at, ExitCode: 124, TimedOut: true, ExecutionFailed: true}}},
		RestartBootID: boot, StoppedBootID: boot, StoppedByUser: true,
		RestartCount: 2, RestartAt: &at, LogLocking: true,
		CleanupFailures: []string{"cgroup.remove"}, Generation: 3, LaunchAt: &at,
		PreviousExit: &container.ExecutionResult{ExitReason: "exited", CleanupFailures: []string{"run.remove"}, Generation: 2, StartedAt: &at, FinishedAt: &at, ExitCode: &code},
		StopTimeout:  &grace, RetainRootFS: true, Version: 1,
		ID: strings.Repeat("a", 32), BootID: boot, Name: "cp-demo-app", State: container.StateFailed,
		CreatedAt: at, StartedAt: &at, FinishedAt: &at, ExitCode: &code,
		Error: "legacy-error", RunPath: "/legacy-run", Cgroup: "/legacy-cgroup", LogTruncated: true,
		Command: []string{"echo", "", "line\nbreak", "--publish"},
		Publish: []config.PortMapping{{HostIP: "127.0.0.1", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}},
	}
}

func TestContainerListingJSONPreservesExistingFieldsAndOmissionRules(t *testing.T) {
	empty := container.Record{Command: []string{}, Labels: map[string]string{}, Publish: []config.PortMapping{}, CleanupFailures: []string{}, Health: &container.Health{Checks: []container.HealthResult{}}}
	zero, grace := 0, time.Duration(0)
	zeroPointers := container.Record{ExitCode: &zero, StopTimeout: &grace, PreviousExit: &container.ExecutionResult{ExitCode: &zero}}
	for _, records := range [][]container.Record{nil, {}, {{}}, {empty}, {zeroPointers}, {completeListingRecord(), empty, zeroPointers}} {
		var out bytes.Buffer
		if err := writeRecords(&out, records, true); err != nil {
			t.Fatal(err)
		}
		legacy := records
		if legacy == nil {
			legacy = []container.Record{}
		}
		want, err := json.Marshal(legacy)
		if err != nil {
			t.Fatal(err)
		}
		if out.String() != string(want)+"\n" {
			t.Fatalf("listing JSON changed:\ngot  %s\nwant %s", out.Bytes(), want)
		}
	}
	if err := writeRecords(failingWriter{}, []container.Record{completeListingRecord()}, true); err == nil {
		t.Fatal("listing output failure was lost")
	}
}

func TestContainerListingJSONContractHasExplicitFieldSet(t *testing.T) {
	var out bytes.Buffer
	if err := writeRecords(&out, []container.Record{completeListingRecord()}, true); err != nil {
		t.Fatal(err)
	}
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil || len(rows) != 1 {
		t.Fatalf("invalid listing: %s %v", out.Bytes(), err)
	}
	keys := make([]string, 0, len(rows[0]))
	for key := range rows[0] {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	want := "boot_id cgroup cleanup_failures command created_at error exit_code exit_reason finished_at generation health id labels launch_at log_locking log_truncated name oom_killed previous_exit publish restart_at restart_boot_id restart_count retain_rootfs run_path started_at state stop_timeout stopped_boot_id stopped_by_user version"
	if strings.Join(keys, " ") != want {
		t.Fatalf("listing field contract changed: %s", strings.Join(keys, " "))
	}
}

func TestProjectListingPreservesMemberJSONAndDoesNotMutateStorePage(t *testing.T) {
	member := completeListingRecord()
	foreign := completeListingRecord()
	foreign.ID, foreign.Name = strings.Repeat("b", 32), "other"
	foreign.Labels = map[string]string{project.LabelName: "other"}
	page := container.RecordPage{Records: []container.Record{member, foreign}, Next: foreign.ID}
	before, _ := json.Marshal(page)
	var out bytes.Buffer
	if err := writeProjectRecords(&out, page, "demo"); err != nil {
		t.Fatal(err)
	}
	redacted := container.Record{ID: foreign.ID, Name: foreign.Name, State: foreign.State, Generation: foreign.Generation, CreatedAt: foreign.CreatedAt}
	want, _ := json.Marshal(container.RecordPage{Records: []container.Record{member, redacted}, Next: foreign.ID})
	if out.String() != string(want)+"\n" {
		t.Fatalf("project listing changed:\ngot %s\nwant %s", out.Bytes(), want)
	}
	after, _ := json.Marshal(page)
	if !bytes.Equal(before, after) {
		t.Fatal("serializing a project page mutated its source records")
	}
}

func TestProjectListingTransportDropsFieldsOutsideItsContract(t *testing.T) {
	member := completeListingRecord()
	data, _ := json.Marshal(member)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	fields["future_storage_metadata"] = json.RawMessage(`"private-value"`)
	body, _ := json.Marshal(map[string]any{"records": []any{fields}})
	call := projectRecordCalls("demo", func(context.Context, ...string) ([]byte, error) { return body, nil })
	got, err := call(context.Background(), "ps", "-a", "--json", "--filter", "label="+project.LabelName+"=demo")
	if err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal([]container.Record{member})
	if !bytes.Equal(got, want) {
		t.Fatalf("transport did not preserve its explicit contract:\ngot %s\nwant %s", got, want)
	}
}
