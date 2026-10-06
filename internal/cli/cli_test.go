package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mingo-liu/mini-docker/internal/container"
)

func TestParseCommandBoundaries(t *testing.T) {
	r, err := Parse([]string{"run", "--rootfs", "/tmp/rootfs", "--timeout", "2s", "--", "/bin/sh", "-c", "echo '$HOME'; exit 7"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Config.Memory != 128<<20 || r.Config.PidsLimit != 64 || r.Config.Timeout != 2*time.Second {
		t.Fatalf("unexpected defaults: %+v", r.Config)
	}
	want := []string{"/bin/sh", "-c", "echo '$HOME'; exit 7"}
	if !reflect.DeepEqual(r.Config.Command, want) {
		t.Fatalf("command changed: %q", r.Config.Command)
	}
}

func TestParseRejectsInvalidOptions(t *testing.T) {
	cases := [][]string{
		{"run", "--rootfs", "/tmp/r", "/bin/sh"},
		{"run", "--rootfs", "/tmp/r", "--"},
		{"run", "--", "echo"},
		{"run", "--rootfs", "/tmp/r", "--hostname", "-bad", "--", "echo"},
		{"run", "--rootfs", "/tmp/r", "--pids-limit", "0", "--", "echo"},
		{"run", "--rootfs", "/tmp/r", "--timeout", "-1s", "--", "echo"},
		{"doctor", "--rootfs", "/tmp/r", "--", "echo"},
		{"unknown"},
	}
	for _, args := range cases {
		if _, err := Parse(args); err == nil {
			t.Errorf("accepted invalid arguments: %q", args)
		}
	}
}

func TestMemoryBoundaries(t *testing.T) {
	for input, want := range map[string]int64{"1": 1, "64m": 64 << 20, "2G": 2 << 30, "1024k": 1 << 20} {
		got, err := ParseMemory(input)
		if err != nil || got != want {
			t.Errorf("ParseMemory(%q) = %d, %v; want %d", input, got, err, want)
		}
	}
	for _, input := range []string{"", "0", "-1", "1.5m", "1mb", "8589934592g", "9223372036854775808", " 1m"} {
		if _, err := ParseMemory(input); err == nil {
			t.Errorf("accepted %q", input)
		}
	}
}

func TestHelpDoesNotRequireRootFS(t *testing.T) {
	for _, args := range [][]string{nil, {"help"}, {"run", "--help"}, {"doctor", "-h"}, {"ps", "--help"}, {"logs", "-h"}, {"stop", "--help"}, {"rm", "-h"}, {"exec", "--help"}} {
		r, err := Parse(args)
		if err != nil || r.Action != "help" {
			t.Errorf("Parse(%q) = %+v, %v", args, r, err)
		}
	}
}

func TestExecOptions(t *testing.T) {
	r, err := Parse([]string{"exec", "-i", "--env", "COLOR=blue", "--env", "COLOR=red", "--env", "EMPTY=", "--workdir", "/work/../tmp/", "--timeout", "2s", "worker", "--", "/bin/sh", "-c", "echo '$HOME'; exit 7", "--env", "COMMAND=value", "-it"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Action != "exec" || r.Reference != "worker" || !r.Exec.Interactive || r.Exec.Workdir != "/tmp" || r.Exec.Timeout != 2*time.Second {
		t.Fatalf("unexpected exec options: %+v", r)
	}
	if !reflect.DeepEqual(r.Exec.Env, []string{"COLOR=blue", "COLOR=red", "EMPTY="}) {
		t.Fatalf("environment changed: %q", r.Exec.Env)
	}
	if !reflect.DeepEqual(r.Exec.Command, []string{"/bin/sh", "-c", "echo '$HOME'; exit 7", "--env", "COMMAND=value", "-it"}) {
		t.Fatalf("command changed: %q", r.Exec.Command)
	}
	defaults, err := Parse([]string{"exec", "worker", "--", "true"})
	if err != nil || defaults.Exec.Interactive || defaults.Exec.TTY || defaults.Exec.Workdir != "" || defaults.Exec.Timeout != 0 || defaults.Exec.Env != nil {
		t.Fatalf("unexpected exec defaults: %+v, %v", defaults, err)
	}
	for _, flag := range []string{"-i", "--interactive", "--interactive=false"} {
		got, err := Parse([]string{"exec", flag, "worker", "--", "cat"})
		if err != nil || got.Exec.Interactive != (flag != "--interactive=false") {
			t.Fatalf("Parse(%q): %+v, %v", flag, got, err)
		}
	}
}

func TestExecRejectsInvalidArguments(t *testing.T) {
	for _, args := range [][]string{
		{"exec"}, {"exec", "worker"}, {"exec", "worker", "true"},
		{"exec", "worker", "--"}, {"exec", "worker", "--", ""},
		{"exec", "a", "b", "--", "true"}, {"exec", "", "--", "true"},
		{"exec", "worker", "-i", "--", "cat"}, {"exec", "worker", "--env", "K=V", "--", "true"},
		{"exec", "../worker", "--", "true"}, {"exec", "worker\x00", "--", "true"},
		{"exec", "--env", "KEY", "worker", "--", "true"},
		{"exec", "--env", "BAD-KEY=v", "worker", "--", "true"},
		{"exec", "--env", "KEY=a\x00b", "worker", "--", "true"},
		{"exec", "--workdir", "relative", "worker", "--", "true"},
		{"exec", "--workdir", "", "worker", "--", "true"},
		{"exec", "--workdir", "/a\x00b", "worker", "--", "true"},
		{"exec", "--timeout", "-1s", "worker", "--", "true"},
		{"exec", "--user", "0", "worker", "--", "true"},
		{"exec", "--rootfs", "/tmp", "worker", "--", "true"},
		{"exec", "worker", "--", "true", "a\x00b"},
	} {
		if _, err := Parse(args); err == nil {
			t.Errorf("accepted invalid exec arguments: %q", args)
		}
	}
}

func TestExecTerminalOptions(t *testing.T) {
	for _, tt := range []struct {
		options     []string
		interactive bool
		tty         bool
	}{
		{nil, false, false},
		{[]string{"-i"}, true, false},
		{[]string{"-t"}, false, true},
		{[]string{"--tty"}, false, true},
		{[]string{"-it"}, true, true},
		{[]string{"-ti"}, true, true},
		{[]string{"-i", "-t"}, true, true},
		{[]string{"--interactive", "--tty"}, true, true},
		{[]string{"--tty=false"}, false, false},
		{[]string{"-it", "-i=false"}, false, true},
		{[]string{"-it", "--tty=false"}, true, false},
	} {
		args := append([]string{"exec"}, tt.options...)
		args = append(args, "worker", "--", "sh", "-it", "-ti", "--tty")
		r, err := Parse(args)
		if err != nil {
			t.Fatalf("Parse(%q): %v", args, err)
		}
		if r.Exec.Interactive != tt.interactive || r.Exec.TTY != tt.tty {
			t.Errorf("Parse(%q): interactive=%v tty=%v; want %v %v", args, r.Exec.Interactive, r.Exec.TTY, tt.interactive, tt.tty)
		}
		if !reflect.DeepEqual(r.Exec.Command, []string{"sh", "-it", "-ti", "--tty"}) {
			t.Errorf("terminal flags changed command: %q", r.Exec.Command)
		}
	}
	for _, options := range [][]string{{"-it=false"}, {"--tty=invalid"}, {"worker", "-it"}} {
		args := append([]string{"exec"}, options...)
		if len(options) < 2 {
			args = append(args, "worker")
		}
		args = append(args, "--", "sh")
		if _, err := Parse(args); err == nil {
			t.Errorf("accepted invalid terminal arguments %q", args)
		}
	}
	for _, value := range []string{"-it", "-ti"} {
		r, err := Parse([]string{"exec", "--env", "VALUE=" + value, "worker", "--", "sh"})
		if err != nil || r.Exec.TTY || r.Exec.Interactive || !reflect.DeepEqual(r.Exec.Env, []string{"VALUE=" + value}) {
			t.Errorf("terminal spelling in environment value was expanded: %+v, %v", r, err)
		}
	}
}

func TestDetachedRunOptions(t *testing.T) {
	for _, detach := range []string{"-d", "--detach"} {
		r, err := Parse([]string{"run", detach, "--name", "task_1.dev-2", "--rootfs", "/tmp/r", "--", "sh", "--detach", "--name", "command-option"})
		if err != nil {
			t.Fatal(err)
		}
		if !r.Detach || r.Name != "task_1.dev-2" {
			t.Fatalf("unexpected detached options: %+v", r)
		}
		if !reflect.DeepEqual(r.Config.Command, []string{"sh", "--detach", "--name", "command-option"}) {
			t.Fatalf("command arguments changed: %q", r.Config.Command)
		}
	}
	for _, options := range [][]string{
		{"--name", "task"},
		{"--detach=false", "--name", "task"},
		{"-d", "--name", ""},
		{"-d", "--name", "../task"},
		{"-d", "--name", "-task"},
		{"-d", "--name", "task space"},
		{"-d", "--name", strings.Repeat("a", 64)},
	} {
		args := append([]string{"run", "--rootfs", "/tmp/r"}, options...)
		args = append(args, "--", "sh")
		if _, err := Parse(args); err == nil {
			t.Errorf("accepted invalid detached arguments: %q", args)
		}
	}
	r, err := Parse([]string{"run", "-d", "--rootfs", "/tmp/r", "--", "sh"})
	if err != nil || !r.Detach || r.Name != "" {
		t.Fatalf("unnamed detached container: %+v, %v", r, err)
	}
}

func TestTerminalRunOptions(t *testing.T) {
	tests := []struct {
		name        string
		options     []string
		interactive bool
		tty         bool
	}{
		{"legacy stdin", nil, true, false},
		{"short interactive", []string{"-i"}, true, false},
		{"long interactive", []string{"--interactive"}, true, false},
		{"short terminal", []string{"-t"}, false, true},
		{"long terminal", []string{"--tty"}, false, true},
		{"combined", []string{"-it"}, true, true},
		{"reverse combined", []string{"-ti"}, true, true},
		{"separate", []string{"-i", "-t"}, true, true},
		{"long combined", []string{"--interactive", "--tty"}, true, true},
		{"disabled input", []string{"--interactive=false"}, false, false},
		{"disabled terminal input", []string{"-it", "-i=false"}, false, true},
		{"disabled terminal", []string{"--tty=false"}, true, false},
		{"detached", []string{"-d"}, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := append([]string{"run", "--rootfs", "/tmp/r"}, tt.options...)
			args = append(args, "--", "sh", "-it", "-ti", "--tty")
			r, err := Parse(args)
			if err != nil {
				t.Fatal(err)
			}
			if r.Config.Interactive != tt.interactive || r.Config.TTY != tt.tty {
				t.Fatalf("terminal options = interactive:%v tty:%v; want %v %v", r.Config.Interactive, r.Config.TTY, tt.interactive, tt.tty)
			}
			if !reflect.DeepEqual(r.Config.Command, []string{"sh", "-it", "-ti", "--tty"}) {
				t.Fatalf("command arguments changed: %q", r.Config.Command)
			}
		})
	}
}

func TestTerminalFlagsPreserveOptionValues(t *testing.T) {
	for _, value := range []string{"-it", "-ti"} {
		r, err := Parse([]string{"run", "--rootfs", value, "--env", "VALUE=" + value, "--", "sh"})
		if err != nil {
			t.Fatal(err)
		}
		if r.Config.RootFS != value || r.Config.TTY || !r.Config.Interactive || !reflect.DeepEqual(r.Config.Env, []string{"VALUE=" + value}) {
			t.Fatalf("flag value was expanded: %+v", r.Config)
		}
		r, err = Parse([]string{"run", "--rootfs=" + value, "-it", "--", "sh"})
		if err != nil || r.Config.RootFS != value || !r.Config.TTY || !r.Config.Interactive {
			t.Fatalf("inline flag value = %+v, %v", r.Config, err)
		}
	}
}

func TestTerminalOptionsRejectInvalidInput(t *testing.T) {
	for _, options := range [][]string{
		{"-d", "-i"}, {"--detach", "--interactive"},
		{"-d", "-t"}, {"--detach", "--tty"}, {"-d", "-it"},
		{"-dit"}, {"-it=false"}, {"--interactive=invalid"}, {"--tty=invalid"},
	} {
		args := append([]string{"run", "--rootfs", "/tmp/r"}, options...)
		args = append(args, "--", "sh")
		if _, err := Parse(args); err == nil {
			t.Errorf("accepted invalid terminal options: %q", args)
		}
	}
}

func TestManagementCommands(t *testing.T) {
	for _, args := range [][]string{{"ps", "-a", "--json"}, {"ps", "--all", "--json"}} {
		r, err := Parse(args)
		if err != nil || r.Action != "ps" || !r.All || !r.JSON {
			t.Fatalf("Parse(%q) = %+v, %v", args, r, err)
		}
	}
	r, err := Parse([]string{"ps"})
	if err != nil || r.All || r.JSON {
		t.Fatalf("unexpected ps defaults: %+v, %v", r, err)
	}
	for _, action := range []string{"stop", "logs", "rm"} {
		r, err := Parse([]string{action, "task_1"})
		if err != nil || r.Action != action || r.Reference != "task_1" {
			t.Fatalf("Parse(%q) = %+v, %v", action, r, err)
		}
		if action == "logs" && (r.Tail != -1 || r.Follow) {
			t.Fatalf("unexpected logs defaults: %+v", r)
		}
	}
	for _, follow := range []string{"-f", "--follow"} {
		r, err := Parse([]string{"logs", "--tail", "3", follow, "task"})
		if err != nil || r.Tail != 3 || !r.Follow || r.Reference != "task" {
			t.Fatalf("unexpected log options: %+v, %v", r, err)
		}
	}
	for _, tail := range []string{"0", "1000000"} {
		if _, err := Parse([]string{"logs", "--tail", tail, "task"}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestManagementRejectsInvalidArguments(t *testing.T) {
	for _, args := range [][]string{
		{"ps", "task"}, {"ps", "--rootfs", "/tmp/r"},
		{"stop"}, {"stop", "a", "b"}, {"stop", ""}, {"stop", "--force", "task"},
		{"rm"}, {"rm", "a", "b"}, {"rm", "--force", "task"}, {"rm", "../task"},
		{"logs"}, {"logs", "a", "b"}, {"logs", "--tail", "-1", "task"},
		{"logs", "--tail", "1000001", "task"}, {"logs", "--tail", "abc", "task"},
		{"logs", "task", "--tail", "1"}, {"logs", "task", "-f"},
		{"logs", "task\x00"}, {"logs", "/task"}, {"rm", "."}, {"rm", ".."},
	} {
		if _, err := Parse(args); err == nil {
			t.Errorf("accepted invalid management arguments: %q", args)
		}
	}
}

func TestContainerListingOutput(t *testing.T) {
	code := 7
	created := time.Date(2026, 10, 6, 10, 11, 12, 0, time.FixedZone("local", 3600))
	records := []container.Record{{ID: "abc123", Name: "task", State: "exited", CreatedAt: created, ExitCode: &code, Command: []string{"sh", "-c", "echo hello\nexit 7"}}}
	var out bytes.Buffer
	if err := writeRecords(&out, records, false); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"ID", "NAME", "STATUS", "EXIT", "CREATED", "COMMAND", "abc123", "task", "exited", "7", "2026-10-06T09:11:12Z", `sh -c "echo hello\nexit 7"`} {
		if !strings.Contains(text, want) {
			t.Errorf("listing missing %q: %s", want, text)
		}
	}
	if strings.Count(text, "\n") != 2 {
		t.Fatalf("command escaped table boundaries: %q", text)
	}
	out.Reset()
	if err := writeRecords(&out, records, true); err != nil {
		t.Fatal(err)
	}
	var decoded []container.Record
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 1 || decoded[0].ID != "abc123" || decoded[0].ExitCode == nil || *decoded[0].ExitCode != 7 || !reflect.DeepEqual(decoded[0].Command, records[0].Command) {
		t.Fatalf("JSON listing did not preserve records: %+v", decoded)
	}
	out.Reset()
	if err := writeRecords(&out, nil, true); err != nil || out.String() != "[]\n" {
		t.Fatalf("empty JSON listing = %q, %v", out.String(), err)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("closed output") }

func TestContainerListingPropagatesWriteFailure(t *testing.T) {
	for _, asJSON := range []bool{false, true} {
		if err := writeRecords(failingWriter{}, nil, asJSON); err == nil {
			t.Error("listing ignored output failure")
		}
	}
}

func TestExecutionOptions(t *testing.T) {
	r, err := Parse([]string{"run", "--rootfs", "/tmp/r", "--cpus", ".125", "--env", "COLOR=blue", "--env", "COLOR=red", "--env", "EMPTY=", "--workdir", "/work/../tmp/", "--user", "123:456", "--read-only", "--", "sh", "--cpus", "2", "--env", "HOST=value"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Config.CPUQuota != 12500 || r.Config.Workdir != "/tmp" || !r.Config.ReadOnly || r.Config.User == nil || r.Config.User.UID != 123 || r.Config.User.GID != 456 {
		t.Fatalf("unexpected execution options: %+v", r.Config)
	}
	if !reflect.DeepEqual(r.Config.Env, []string{"COLOR=blue", "COLOR=red", "EMPTY="}) {
		t.Fatalf("environment assignments changed: %q", r.Config.Env)
	}
	if !reflect.DeepEqual(r.Config.Command, []string{"sh", "--cpus", "2", "--env", "HOST=value"}) {
		t.Fatalf("command arguments changed: %q", r.Config.Command)
	}
	defaults, err := Parse([]string{"run", "--rootfs", "/tmp/r", "--", "sh"})
	if err != nil {
		t.Fatal(err)
	}
	if defaults.Config.CPUQuota != 0 || defaults.Config.Env != nil || defaults.Config.Workdir != "/" || defaults.Config.User != nil || defaults.Config.ReadOnly {
		t.Fatalf("unexpected defaults: %+v", defaults.Config)
	}
}

func TestExecutionOptionsRejectInvalidInput(t *testing.T) {
	for _, option := range [][]string{
		{"--cpus", "NaN"}, {"--cpus", "-1"}, {"--cpus", "0.001"},
		{"--env", "KEY"}, {"--env", "BAD-KEY=value"}, {"--env", "KEY=a\x00b"},
		{"--workdir", "relative"}, {"--workdir", ""}, {"--workdir", "/a\x00b"},
		{"--user", ""}, {"--user", "root"}, {"--user", "1:2:3"},
	} {
		args := append([]string{"run", "--rootfs", "/tmp/r"}, option...)
		args = append(args, "--", "echo")
		if _, err := Parse(args); err == nil {
			t.Errorf("accepted invalid options: %q", args)
		}
	}
	if _, err := Parse([]string{"run", "--rootfs", "/tmp/r", "--", "echo", "a\x00b"}); err == nil {
		t.Error("accepted NUL in command argument")
	}
}

func TestCPUBoundaries(t *testing.T) {
	for input, want := range map[string]int64{
		"0": 0, "0.000": 0, "0.01": 1000, ".5": 50000, "1": 100000, "2.125": 212500, "1000": 100000000,
	} {
		got, err := ParseCPUs(input)
		if err != nil || got != want {
			t.Errorf("ParseCPUs(%q) = %d, %v; want %d", input, got, err, want)
		}
	}
	for _, input := range []string{"", "NaN", "Inf", "1e2", "-1", "+1", " 1", "1 ", "1.", ".", "0.001", "1.0001", "1000.001", "1001", "9223372036854775808"} {
		if _, err := ParseCPUs(input); err == nil {
			t.Errorf("accepted invalid CPU count %q", input)
		}
	}
}

func TestUserBoundaries(t *testing.T) {
	for _, tt := range []struct {
		input string
		uid   uint32
		gid   uint32
	}{
		{"0", 0, 0}, {"123", 123, 123}, {"123:456", 123, 456}, {"4294967294:0", 4294967294, 0},
	} {
		got, err := ParseUser(tt.input)
		if err != nil || got.UID != tt.uid || got.GID != tt.gid {
			t.Errorf("ParseUser(%q) = %+v, %v", tt.input, got, err)
		}
	}
	for _, input := range []string{"", "root", "-1", "+1", ":1", "1:", "1:2:3", "1:group", "4294967295", "0:4294967295", "4294967296", " 1"} {
		if _, err := ParseUser(input); err == nil {
			t.Errorf("accepted invalid identity %q", input)
		}
	}
}

func TestInspectionAndStatsParsing(t *testing.T) {
	for _, args := range [][]string{{"inspect", "worker"}, {"stats", "worker"}, {"stats", "--json", "--interval", "250ms", "worker"}} {
		got, err := Parse(args)
		if err != nil || got.Reference != "worker" {
			t.Fatalf("parse %v: %+v, %v", args, got, err)
		}
		if got.Action == "stats" && (got.Interval < 10*time.Millisecond || got.Interval > time.Minute) {
			t.Fatalf("interval: %v", got.Interval)
		}
	}
	for _, args := range [][]string{{"inspect"}, {"inspect", "worker", "extra"}, {"inspect", "--env", "worker"}, {"inspect", "../worker"}, {"stats"}, {"stats", "worker", "--json"}, {"stats", "--interval", "0", "worker"}, {"stats", "--interval", "-1s", "worker"}, {"stats", "--interval", "1ms", "worker"}, {"stats", "--interval", "2m", "worker"}, {"stats", "--interval", "bad", "worker"}} {
		if got, err := Parse(args); err == nil {
			t.Fatalf("accepted %v: %+v", args, got)
		}
	}
}

func TestWriteStatsUnavailableAndZero(t *testing.T) {
	var out bytes.Buffer
	stats := container.Statistics{ID: "id", Name: "worker", State: "exited", MemoryLimitBytes: 1024}
	if err := writeStats(&out, stats, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"memory_bytes":null`) || !strings.Contains(out.String(), `"cpu_percent":null`) {
		t.Fatalf("JSON: %s", out.String())
	}
	out.Reset()
	if err := writeStats(&out, stats, false); err != nil || strings.Count(out.String(), "N/A") != 2 {
		t.Fatalf("unavailable: %s, %v", out.String(), err)
	}
	memory, cpu := uint64(0), float64(0)
	stats.MemoryBytes, stats.CPUPercent = &memory, &cpu
	out.Reset()
	if err := writeStats(&out, stats, false); err != nil || strings.Contains(out.String(), "N/A") || !strings.Contains(out.String(), "0.00%") {
		t.Fatalf("zero: %s, %v", out.String(), err)
	}
}

func TestRunMountOptions(t *testing.T) {
	request, err := Parse([]string{"run", "--rootfs", "/template", "--mount", "type=bind,source=/srv/a,target=/data", "--mount", "type=bind,source=/srv/b,target=/config,readonly", "--", "sh"})
	if err != nil {
		t.Fatal(err)
	}
	if len(request.Config.Mounts) != 2 || request.Config.Mounts[0].Source != "/srv/a" || request.Config.Mounts[0].ReadOnly || !request.Config.Mounts[1].ReadOnly {
		t.Fatalf("mounts=%+v", request.Config.Mounts)
	}
	for _, args := range [][]string{
		{"run", "--rootfs", "/template", "--mount", "type=bind,source=/srv/a,target=/data", "--mount", "type=bind,source=/srv/b,target=/data/child", "--", "sh"},
		{"run", "--rootfs", "/template", "--mount", "type=bind,source=/srv/a,target=/proc", "--", "sh"},
		{"doctor", "--rootfs", "/template", "--mount", "type=bind,source=/srv/a,target=/data"},
		{"exec", "--mount", "type=bind,source=/srv/a,target=/data", "worker", "--", "sh"},
	} {
		if _, err := Parse(args); err == nil {
			t.Errorf("accepted %v", args)
		}
	}
}

func TestImageCommands(t *testing.T) {
	id := "sha256:" + strings.Repeat("a", 64)
	for _, args := range [][]string{{"image", "import", "/template"}, {"image", "ls"}, {"image", "ls", "--json"}, {"image", "rm", id}, {"run", "--image", id, "--", "sh"}} {
		if _, err := Parse(args); err != nil {
			t.Fatalf("parse %v: %v", args, err)
		}
	}
	for _, args := range [][]string{{"image"}, {"image", "other"}, {"image", "import"}, {"image", "ls", "extra"}, {"image", "rm", "../outside"}, {"image", "rm", id, "extra"}, {"run", "--rootfs", "/template", "--image", id, "--", "sh"}, {"run", "--image", "", "--", "sh"}, {"run", "--rootfs", "", "--image", id, "--", "sh"}, {"run", "--image", "sha256:abc", "--", "sh"}, {"doctor", "--image", id}} {
		if _, err := Parse(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestLifecycleOptions(t *testing.T) {
	for _, args := range [][]string{{"wait", "worker"}, {"start", "worker"}, {"restart", "worker"}, {"stop", "--timeout", "0s", "worker"}, {"restart", "--timeout", "1m", "worker"}, {"run", "--rootfs", "/template", "--stop-timeout", "20ms", "--", "sh"}} {
		if _, err := Parse(args); err != nil {
			t.Fatalf("parse %v: %v", args, err)
		}
	}
	for _, args := range [][]string{{"wait"}, {"wait", "a", "b"}, {"start", "../worker"}, {"stop", "--timeout", "-1s", "worker"}, {"restart", "--timeout", "61s", "worker"}, {"stop", "worker", "--timeout", "1s"}, {"run", "--rootfs", "/template", "--stop-timeout", "61s", "--", "sh"}} {
		if _, err := Parse(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	request, err := Parse([]string{"stop", "--timeout", "0s", "worker"})
	if err != nil || request.StopTimeout == nil || *request.StopTimeout != 0 {
		t.Fatal("zero stop timeout was treated as default")
	}
}
