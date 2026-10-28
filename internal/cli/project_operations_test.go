package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mingo-liu/casklet/internal/container"
	"github.com/mingo-liu/casklet/internal/project"
)

func operationRecords() []container.Record {
	return []container.Record{
		{ID: strings.Repeat("a", 32), Name: "cp-demo-db", CreatedAt: time.Unix(1, 0), Labels: map[string]string{project.LabelName: "demo", project.LabelService: "db", project.LabelSpec: "spec"}},
		{ID: strings.Repeat("b", 32), Name: "cp-demo-api", CreatedAt: time.Unix(2, 0), Labels: map[string]string{project.LabelName: "demo", project.LabelService: "api", project.LabelSpec: "spec"}},
	}
}
func TestProjectOperationsParsingAndHostEnvironment(t *testing.T) {
	for _, args := range [][]string{{"project", "start", "demo"}, {"project", "stop", "--timeout", "2s", "demo", "api", "db"}, {"project", "restart", "demo", "api"}, {"project", "exec", "-it", "demo", "api", "--", "sh"}} {
		if _, err := Parse(args); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	for _, args := range [][]string{{"project", "exec", "demo", "--", "sh"}, {"project", "exec", "demo", "api"}, {"project", "start"}, {"project", "stop", "--timeout", "2m", "demo"}, {"project", "restart", "demo", "api", "api"}, {"project", "start", "demo", "UPPER"}} {
		if _, err := Parse(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	filename := filepath.Join(t.TempDir(), "env")
	os.WriteFile(filename, []byte("VALUE=from-file\n"), 0600)
	r, err := Parse([]string{"project", "exec", "--env-file", filename, "--env", "VALUE=explicit", "demo", "api", "--", "printf", "--help"})
	if err != nil || !reflect.DeepEqual(r.Exec.Command, []string{"printf", "--help"}) || !reflect.DeepEqual(r.Exec.Env, []string{"VALUE=from-file", "VALUE=explicit"}) {
		t.Fatalf("exec config %+v %v", r, err)
	}
}
func TestProjectLifecyclePinsSelectionAndAggregatesFailures(t *testing.T) {
	records := operationRecords()
	var calls [][]string
	var out bytes.Buffer
	call := func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		if args[len(args)-1] == records[1].ID {
			return nil, errors.New("failed")
		}
		return nil, nil
	}
	err := executeProjectLifecycle(context.Background(), Request{Action: "project-stop", Reference: "demo"}, records, &out, call)
	if err == nil || len(calls) != 2 || calls[0][1] != records[1].ID || out.String() != records[0].ID+"\n" {
		t.Fatalf("partial %v %v %s", err, calls, &out)
	}
	calls = nil
	err = executeProjectLifecycle(context.Background(), Request{Action: "project-start", Reference: "demo", Services: []string{"missing"}}, records, &out, call)
	if err == nil || len(calls) != 0 {
		t.Fatal("mutated invalid selection")
	}
	records[0].Name = "renamed"
	err = executeProjectLifecycle(context.Background(), Request{Action: "project-start", Reference: "demo"}, records, &out, call)
	if err == nil || len(calls) != 0 {
		t.Fatal("adopted renamed service")
	}
}
func TestProjectExecResolvesLogicalServiceAndKeepsWorkloadOperands(t *testing.T) {
	records := operationRecords()
	backend := project.CommandBackend{Call: func(context.Context, ...string) ([]byte, error) { return json.Marshal(records) }}
	r, err := Parse([]string{"project", "exec", "--env", "A=space value", "demo", "api", "--", "echo", "-t", "--help"})
	if err != nil {
		t.Fatal(err)
	}
	args, err := projectExecArguments(context.Background(), r, backend)
	expected := []string{"exec", "--env", "A=space value", records[1].ID, "--", "echo", "-t", "--help"}
	if err != nil || !reflect.DeepEqual(args, expected) {
		t.Fatalf("args %v %v", args, err)
	}
}
