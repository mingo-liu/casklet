package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
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
		call := newProjectBackend("demo", func(ctx context.Context, args ...string) ([]byte, error) {
			calls++
			r, err := Parse(args)
			if err != nil || r.Action != "project-records" || r.All == scoped {
				t.Fatalf("selection: %v %+v %v", args, r, err)
			}
			out := &boundedProjectOutput{limit: 8 << 20}
			if err := writeProjectRecords(out, testRecordPage(records, r.Cursor, r.Reference, r.All, nil), r.Reference); err != nil || out.overflow {
				t.Fatalf("page overflow: %v", err)
			}
			if bytes.Contains(out.data, []byte("unrelated-private")) || bytes.Contains(out.data, []byte(strings.Repeat(" ", 1024))) {
				t.Fatal("unrelated command, labels or diagnostics crossed transport")
			}
			return out.data, nil
		})
		runner := project.Runner{Backend: call}
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
	for _, count := range []int{64, 65} {
		records := make([]container.Record, count+1)
		for i := range records {
			records[i] = container.Record{ID: fmt.Sprintf("%032x", i+1), CreatedAt: time.Unix(int64(100-i), 0), Labels: map[string]string{project.LabelName: "demo"}}
		}
		// An unrelated trailing ID can require an empty final scoped page.
		records[count].Labels = nil
		calls := 0
		call := newProjectBackend("demo", func(_ context.Context, args ...string) ([]byte, error) {
			calls++
			r, err := Parse(args)
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			err = writeProjectRecords(&out, testRecordPage(records, r.Cursor, r.Reference, r.All, nil), r.Reference)
			return out.Bytes(), err
		})
		runner := project.Runner{Backend: call}
		got, err := runner.Records(context.Background(), "demo")
		if err != nil || len(got) != count || calls != 2 {
			t.Fatalf("listing: %d records, %d calls, %v", len(got), calls, err)
		}
		for i := 1; i < len(got); i++ {
			if !got[i].CreatedAt.After(got[i-1].CreatedAt) {
				t.Fatal("ID traversal changed reverse-creation rollback order")
			}
		}
	}
}

func TestProjectRecordPagesRejectMalformedContinuationsAndHonorCancellation(t *testing.T) {
	for _, body := range []string{
		`{`, `null`, `{"records":null}`, `{"records":[],"next":"bad"}`,
		`{"records":[{"id":"bad"}]}`, `{"records":[{"id":"00000000000000000000000000000001"},{"id":"00000000000000000000000000000001"}]}`,
	} {
		call := newProjectBackend("demo", func(context.Context, ...string) ([]byte, error) { return []byte(body), nil })
		if _, err := call.Records(context.Background(), ""); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	count := 0
	call := newProjectBackend("demo", func(context.Context, ...string) ([]byte, error) {
		count++
		cancel()
		records := make([]container.Record, projectRecordPageSize)
		for i := range records {
			records[i].ID = fmt.Sprintf("%032x", i+1)
		}
		return json.Marshal(projectRecordPage{Records: containerListings(records), Next: records[len(records)-1].ID})
	})
	if _, err := call.Records(ctx, ""); !errors.Is(err, context.Canceled) || count != 1 {
		t.Fatalf("cancellation: calls=%d err=%v", count, err)
	}
	for _, args := range [][]string{{"__project-records"}, {"__project-records", "demo", "unknown"}, {"__project-records", "../demo", "project"}, {"__project-records", "demo", "inventory", "bad"}} {
		if _, err := Parse(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	if err := writeProjectRecords(failingWriter{}, container.RecordPage{Records: []container.Record{}}, "demo"); err == nil {
		t.Fatal("output error lost")
	}
	var out bytes.Buffer
	if err := writeProjectRecords(&out, container.RecordPage{Records: []container.Record{}}, "demo"); err != nil || out.String() != "{\"records\":[]}\n" {
		t.Fatalf("empty page: %s %v", out.String(), err)
	}
}

func TestProjectInventoryPreservesUnownedNameConflicts(t *testing.T) {
	for _, labels := range []map[string]string{nil, {project.LabelName: "other"}, {project.LabelName: "demo", project.LabelService: "wrong"}} {
		record := container.Record{ID: strings.Repeat("1", 32), Name: "cp-demo-app", State: container.StateExited, Labels: labels}
		call := newProjectBackend("demo", func(_ context.Context, args ...string) ([]byte, error) {
			if args[0] != "__project-records" {
				t.Fatalf("ownership preflight reached mutation/resource call: %v", args)
			}
			var out bytes.Buffer
			err := writeProjectRecords(&out, container.RecordPage{Records: []container.Record{record}}, "demo")
			return out.Bytes(), err
		})
		runner := project.Runner{Backend: call}
		_, err := runner.Up(context.Background(), project.Plan{Name: "demo", Services: []project.Service{{Key: "app", Name: record.Name, Fingerprint: "spec"}}}, project.UpOptions{})
		if err == nil || !strings.Contains(err.Error(), "ownership") {
			t.Fatalf("name conflict was lost: %v", err)
		}
	}
}

// Fake the guest storage pages for transport tests. Linux storage tests exercise
// the actual disk cursor, read bounds, selection and lifecycle refresh.
func testRecordPage(records []container.Record, cursor, name string, inventory bool, excluded []string) container.RecordPage {
	records = append([]container.Record(nil), records...)
	sort.Slice(records, func(i, j int) bool { return records[i].ID < records[j].ID })
	skip := map[string]bool{}
	for _, id := range excluded {
		skip[id] = true
	}
	page := container.RecordPage{Records: []container.Record{}}
	for _, record := range records {
		if record.ID <= cursor || skip[record.ID] {
			continue
		}
		if len(page.Records) == container.RecordPageSize {
			page.Next = page.Records[len(page.Records)-1].ID
			break
		}
		if inventory || record.Labels[project.LabelName] == name {
			page.Records = append(page.Records, record)
		}
	}
	return page
}
