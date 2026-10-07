package container

import (
	"strings"
	"testing"
	"time"
)

func TestValidateName(t *testing.T) {
	for _, name := range []string{"web", "A.test_1-2", strings.Repeat("a", 63)} {
		if err := ValidateName(name); err != nil {
			t.Errorf("valid name %q rejected: %v", name, err)
		}
	}
	for _, name := range []string{"", ".", "..", "../web", "/web", "-web", "web/name", "web\nname", strings.Repeat("a", 64), strings.Repeat("a", 32)} {
		if err := ValidateName(name); err == nil {
			t.Errorf("unsafe or ambiguous name %q accepted", name)
		}
	}
}

func TestRecordValidation(t *testing.T) {
	id := strings.Repeat("a", 32)
	valid := Record{Version: 1, ID: id, Name: "web", State: StateCreated, CreatedAt: time.Now(), Command: []string{"/bin/true"}}
	if err := validateRecord(valid, id); err != nil {
		t.Fatal(err)
	}
	valid.BootID = "abcdef01-2345-6789-abcd-ef0123456789"
	if err := validateRecord(valid, id); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{StateCreated, StateStarting, StateRunning, StateStopping, StateExited, StateFailed} {
		record := valid
		record.State = state
		if err := validateRecord(record, id); err != nil {
			t.Errorf("valid state %s rejected: %v", state, err)
		}
		if record.Terminal() != (state == StateExited || state == StateFailed) {
			t.Errorf("unexpected terminal state for %s", state)
		}
	}
	for _, mutate := range []func(*Record){
		func(r *Record) { r.Version = 2 },
		func(r *Record) { r.BootID = "not-a-UUID" },
		func(r *Record) { r.BootID = "ABCDEF01-2345-6789-abcd-ef0123456789" },
		func(r *Record) { r.ID = "../escape" },
		func(r *Record) { r.Name = "../escape" },
		func(r *Record) { r.State = "invented" },
		func(r *Record) { r.CreatedAt = time.Time{} },
		func(r *Record) { r.Command = nil },
		func(r *Record) { code := 256; r.ExitCode = &code },
		func(r *Record) { r.CleanupFailures = []string{"private /path"} },
		func(r *Record) { r.CleanupFailures = []string{"cgroup.remove", "cgroup.remove"} },
	} {
		record := valid
		mutate(&record)
		if err := validateRecord(record, id); err == nil {
			t.Errorf("invalid record accepted: %+v", record)
		}
	}
}
