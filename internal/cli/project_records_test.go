package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mingo-liu/casklet/internal/container"
	"github.com/mingo-liu/casklet/internal/project"
)

func TestProjectRecordPagesExcludeUnrelatedPayloadsAndPreserveMembers(t *testing.T) {
	records := []container.Record{{ID: fmt.Sprintf("%032x", 1), Name: "cp-demo-app", State: container.StateRunning, Generation: 3,
		Labels: map[string]string{project.LabelName: "demo", project.LabelService: "app", project.LabelSpec: "spec"}, Command: []string{"/bin/sleep", "300"}}}
	for i := 0; i < 256; i++ {
		records = append(records, container.Record{ID: fmt.Sprintf("%032x", i+2), Name: fmt.Sprintf("other-%d", i), State: container.StateCreated, Generation: uint64(i),
			Command: []string{"/bin/sh", "-c", strings.Repeat(" ", 32<<10)}, Labels: map[string]string{"secret": "unrelated-private-label"}, Error: "unrelated-private-error"})
	}
	legacy := &boundedProjectOutput{limit: 8 << 20}
	if err := writeRecords(legacy, records, true); err != nil || !legacy.overflow {
		t.Fatalf("fixture did not reproduce full-list overflow: %v", err)
	}
	for _, scoped := range []bool{false, true} {
		calls := 0
		call := projectRecordCalls("demo", func(ctx context.Context, args ...string) ([]byte, error) {
			calls++
			r, err := Parse(args)
			if err != nil || r.Action != "project-records" || r.All == scoped {
				t.Fatalf("selection: %v %+v %v", args, r, err)
			}
			out := &boundedProjectOutput{limit: 8 << 20}
			if err := writeProjectRecords(out, records, r.Reference, r.Cursor, r.All); err != nil || out.overflow {
				t.Fatalf("page overflow: %v", err)
			}
			if bytes.Contains(out.data, []byte("unrelated-private")) || bytes.Contains(out.data, []byte(strings.Repeat(" ", 1024))) {
				t.Fatal("unrelated command, labels or diagnostics crossed transport")
			}
			return out.data, nil
		})
		runner := project.Runner{Call: call}
		var got []container.Record
		var err error
		if scoped {
			got, err = runner.Records(context.Background(), "demo")
		} else {
			got, err = runner.Records(context.Background())
		}
		if err != nil || len(got) != map[bool]int{false: 257, true: 1}[scoped] || calls != map[bool]int{false: 5, true: 1}[scoped] {
			t.Fatalf("scoped=%v calls=%d records=%d err=%v", scoped, calls, len(got), err)
		}
		if got[0].Generation != 3 || strings.Join(got[0].Command, " ") != "/bin/sleep 300" || got[0].Labels[project.LabelSpec] != "spec" {
			t.Fatalf("member fields changed: %+v", got[0])
		}
		if !scoped && (got[256].Name != "other-255" || got[256].Generation != 255 || len(got[256].Command) != 0) {
			t.Fatalf("unrelated ownership identity changed: %+v", got[256])
		}
	}
}

func TestProjectRecordPagesPreserveCreationOrderForRollback(t *testing.T) {
	records := make([]container.Record, 65)
	for i := range records {
		records[i] = container.Record{ID: fmt.Sprintf("%032x", i+1), CreatedAt: time.Unix(int64(100-i), 0), Labels: map[string]string{project.LabelName: "demo"}}
	}
	call := projectRecordCalls("demo", func(_ context.Context, args ...string) ([]byte, error) {
		r, err := Parse(args)
		if err != nil {
			return nil, err
		}
		var out bytes.Buffer
		err = writeProjectRecords(&out, records, r.Reference, r.Cursor, r.All)
		return out.Bytes(), err
	})
	runner := project.Runner{Call: call}
	got, err := runner.Records(context.Background(), "demo")
	if err != nil || len(got) != 65 {
		t.Fatalf("listing: %d %v", len(got), err)
	}
	for i := 1; i < len(got); i++ {
		if !got[i].CreatedAt.After(got[i-1].CreatedAt) {
			t.Fatal("ID traversal changed reverse-creation rollback order")
		}
	}
}

func TestProjectRecordPagesRejectMalformedContinuationsAndHonorCancellation(t *testing.T) {
	for _, body := range []string{
		`{`, `null`, `{"records":null}`, `{"records":[],"next":"bad"}`,
		`{"records":[{"id":"bad"}]}`, `{"records":[{"id":"00000000000000000000000000000001"},{"id":"00000000000000000000000000000001"}]}`,
	} {
		call := projectRecordCalls("demo", func(context.Context, ...string) ([]byte, error) { return []byte(body), nil })
		if _, err := call(context.Background(), "ps", "-a", "--json"); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	count := 0
	call := projectRecordCalls("demo", func(context.Context, ...string) ([]byte, error) {
		count++
		cancel()
		records := make([]container.Record, projectRecordPageSize)
		for i := range records {
			records[i].ID = fmt.Sprintf("%032x", i+1)
		}
		return json.Marshal(projectRecordPage{Records: records, Next: records[len(records)-1].ID})
	})
	if _, err := call(ctx, "ps", "-a", "--json"); !errors.Is(err, context.Canceled) || count != 1 {
		t.Fatalf("cancellation: calls=%d err=%v", count, err)
	}
	for _, args := range [][]string{{"__project-records"}, {"__project-records", "demo", "unknown"}, {"__project-records", "../demo", "project"}, {"__project-records", "demo", "inventory", "bad"}} {
		if _, err := Parse(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	if err := writeProjectRecords(failingWriter{}, nil, "demo", "", false); err == nil {
		t.Fatal("output error lost")
	}
	var out bytes.Buffer
	if err := writeProjectRecords(&out, nil, "demo", "", false); err != nil || out.String() != "{\"records\":[]}\n" {
		t.Fatalf("empty page: %s %v", out.String(), err)
	}
}

func TestProjectInventoryPreservesUnownedNameConflicts(t *testing.T) {
	for _, labels := range []map[string]string{nil, {project.LabelName: "other"}, {project.LabelName: "demo", project.LabelService: "wrong"}} {
		record := container.Record{ID: strings.Repeat("1", 32), Name: "cp-demo-app", State: container.StateExited, Labels: labels}
		call := projectRecordCalls("demo", func(_ context.Context, args ...string) ([]byte, error) {
			if args[0] != "__project-records" {
				t.Fatalf("ownership preflight reached mutation/resource call: %v", args)
			}
			var out bytes.Buffer
			err := writeProjectRecords(&out, []container.Record{record}, "demo", "", true)
			return out.Bytes(), err
		})
		runner := project.Runner{Call: call}
		_, err := runner.Up(context.Background(), project.Plan{Name: "demo", Services: []project.Service{{Key: "app", Name: record.Name, Fingerprint: "spec"}}}, project.UpOptions{})
		if err == nil || !strings.Contains(err.Error(), "ownership") {
			t.Fatalf("name conflict was lost: %v", err)
		}
	}
}
