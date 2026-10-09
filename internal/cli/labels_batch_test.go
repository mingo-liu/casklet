package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mingo-liu/casklet/internal/container"
)

func TestRunLabelsMergeConfigAndRemainMetadata(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "run.json")
	if err := os.WriteFile(filename, []byte(`{"detach":true,"rootfs":"/template","label":["project=config","role=api"],"command":["true"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := Parse([]string{"run", "--config", filename, "--label", "project=cli", "--label", "empty=", "--label", "project=last"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r.Config.Labels, map[string]string{"project": "last", "role": "api", "empty": ""}) {
		t.Fatalf("labels: %v", r.Config.Labels)
	}
	if strings.Contains(strings.Join(r.Config.CommandEnvironment(), "\n"), "project") {
		t.Fatal("labels became environment")
	}
	for _, args := range [][]string{
		{"run", "--rootfs", "/template", "--label", "project=demo", "--", "true"},
		{"run", "-d", "--rootfs", "/template", "--label", "project", "--", "true"},
		{"run", "-d", "--rootfs", "/template", "--label", "bad key=x", "--", "true"},
	} {
		if _, err := Parse(args); err == nil {
			t.Fatalf("accepted %q", args)
		}
	}
}

func TestBulkManagementParsingAndHelp(t *testing.T) {
	for _, args := range [][]string{
		{"stop", "one", "two"}, {"rm", "one", "two"}, {"stop", "--all"}, {"rm", "--filter", "label=project=demo"},
		{"stop", "--all", "--filter", "label=project=demo", "--filter", "status=running"},
		{"ps", "-a", "--json", "--filter", "health=none", "--filter", "label=project"},
	} {
		if _, err := Parse(args); err != nil {
			t.Fatalf("%q: %v", args, err)
		}
	}
	for _, args := range [][]string{
		{"stop"}, {"rm"}, {"stop", "--all", "one"}, {"rm", "--filter", "label=project=demo", "one"},
		{"stop", "one", "--timeout", "1s"}, {"rm", "one", ""}, {"rm", "one", "../two"}, {"stop", "--filter", "status=stopped"},
	} {
		if _, err := Parse(args); err == nil {
			t.Fatalf("accepted %q", args)
		}
	}
	for _, topic := range []string{"ps", "stop", "rm"} {
		for _, mac := range []bool{false, true} {
			text, err := scopedUsage(topic, mac)
			if err != nil {
				t.Fatal(err)
			}
			for _, fragment := range []string{"--filter", "AND", "label=project=demo"} {
				if !strings.Contains(text, fragment) {
					t.Fatalf("%s help lacks %q", topic, fragment)
				}
			}
		}
	}
}

func TestBatchSnapshotsIDsDeduplicatesAndReportsPartialFailures(t *testing.T) {
	r, err := Parse([]string{"rm", "first", "missing", "last", "alias"})
	if err != nil {
		t.Fatal(err)
	}
	mapping := map[string]string{"first": "id-first", "last": "id-last", "alias": "id-first"}
	var resolved, removed []string
	ops := batchOperations{
		resolve: func(_ context.Context, ref string) (string, error) {
			resolved = append(resolved, ref)
			id, ok := mapping[ref]
			if !ok {
				return "", container.ErrNotFound
			}
			return id, nil
		},
		remove: func(_ context.Context, id string) error {
			if len(resolved) != 4 {
				t.Fatal("mutation began before selection finished")
			}
			removed = append(removed, id)
			mapping["last"] = "replacement" // A name reused during the batch must not redirect it.
			if id == "id-first" {
				return container.ErrNotTerminal
			}
			return nil
		},
	}
	var output bytes.Buffer
	err = executeBatch(context.Background(), r, &output, ops)
	if !errors.Is(err, container.ErrNotFound) || !errors.Is(err, container.ErrNotTerminal) || !strings.Contains(err.Error(), "missing") || !strings.Contains(err.Error(), "id-first") {
		t.Fatalf("aggregate: %v", err)
	}
	if !reflect.DeepEqual(removed, []string{"id-first", "id-last"}) || output.String() != "id-last\n" {
		t.Fatalf("removed %v stdout %q", removed, output.String())
	}
	if strings.Contains(operationError(r, err).Error(), "stop ''") {
		t.Fatal("batch generated an empty-reference hint")
	}
}

func TestBatchSelectorSortsSnapshotAndContinuesAfterFailure(t *testing.T) {
	r, err := Parse([]string{"stop", "--timeout", "0s", "--filter", "label=project=demo"})
	if err != nil {
		t.Fatal(err)
	}
	records := []container.Record{{ID: "c", Labels: map[string]string{"project": "demo"}}, {ID: "b", Labels: map[string]string{"project": "other"}}, {ID: "a", Labels: map[string]string{"project": "demo"}}}
	var ids []string
	ops := batchOperations{
		list: func(_ context.Context, all bool) ([]container.Record, error) {
			if !all {
				t.Fatal("selector omitted retained records")
			}
			return records, nil
		},
		stop: func(_ context.Context, id string, timeout *time.Duration) (container.Record, error) {
			if timeout == nil || *timeout != 0 {
				t.Fatal("lost timeout")
			}
			ids = append(ids, id)
			records = append(records, container.Record{ID: "later", Labels: map[string]string{"project": "demo"}})
			if id == "a" {
				return container.Record{}, container.ErrBusy
			}
			return container.Record{ID: id}, nil
		},
	}
	var output bytes.Buffer
	err = executeBatch(context.Background(), r, &output, ops)
	if !errors.Is(err, container.ErrBusy) || !reflect.DeepEqual(ids, []string{"a", "c"}) || output.String() != "c\n" {
		t.Fatalf("ids %v stdout %q err %v", ids, output.String(), err)
	}
}

func TestBatchCancellationAndOutputFailureStopFurtherMutations(t *testing.T) {
	for _, failure := range []string{"cancel", "output"} {
		t.Run(failure, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			r, _ := Parse([]string{"rm", "--all"})
			count := 0
			ops := batchOperations{
				list: func(context.Context, bool) ([]container.Record, error) {
					return []container.Record{{ID: "a"}, {ID: "b"}}, nil
				},
				remove: func(context.Context, string) error {
					count++
					if failure == "cancel" {
						cancel()
					}
					return nil
				},
			}
			var output io.Writer = io.Discard
			if failure == "output" {
				output = rejectBatchOutput{}
			}
			err := executeBatch(ctx, r, output, ops)
			if err == nil || count != 1 {
				t.Fatalf("mutated %d, error %v", count, err)
			}
			if failure == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		})
	}
}

type rejectBatchOutput struct{}

func (rejectBatchOutput) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestBatchEmptySelectionIsSuccessfulAndOutputsNothing(t *testing.T) {
	r, _ := Parse([]string{"rm", "--filter", "label=project=absent"})
	var out bytes.Buffer
	ops := batchOperations{list: func(context.Context, bool) ([]container.Record, error) { return nil, nil }, remove: func(context.Context, string) error { t.Fatal("removed an unselected record"); return nil }}
	if err := executeBatch(context.Background(), r, &out, ops); err != nil || out.Len() != 0 {
		t.Fatalf("empty: %q %v", out.String(), err)
	}
}
