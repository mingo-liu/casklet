package config

import (
	"reflect"
	"testing"
	"time"
)

func TestExecValidation(t *testing.T) {
	for _, e := range []Exec{
		{Command: []string{"sh"}},
		{Command: []string{"echo", "", "--tty"}, Env: []string{"KEY=", "KEY=a=b"}, Workdir: "/tmp", Timeout: time.Second},
	} {
		if err := e.Validate(); err != nil {
			t.Errorf("valid execution %+v: %v", e, err)
		}
	}
	for _, e := range []Exec{
		{}, {Command: []string{""}}, {Command: []string{"sh", "a\x00b"}},
		{Command: []string{"sh"}, Env: []string{"BAD-KEY=value"}},
		{Command: []string{"sh"}, Env: []string{"KEY"}},
		{Command: []string{"sh"}, Env: []string{"KEY=a\x00b"}},
		{Command: []string{"sh"}, Workdir: "relative"},
		{Command: []string{"sh"}, Workdir: "/a\x00b"},
		{Command: []string{"sh"}, Timeout: -time.Second},
	} {
		if e.Validate() == nil {
			t.Errorf("accepted invalid execution %+v", e)
		}
	}
}

func TestExecInheritsIsolationAndOverridesExecution(t *testing.T) {
	base := Config{
		RootFS: "/template", Hostname: "worker", Memory: 128 << 20, PidsLimit: 64,
		CPUQuota: 50000, Timeout: time.Hour, Env: []string{"COLOR=blue", "BASE=kept"},
		Workdir: "/work", User: &User{UID: 123, GID: 456}, ReadOnly: true,
		Interactive: true, TTY: true, Command: []string{"sleep", "100"},
	}
	e := Exec{Command: []string{"echo", "hello"}, Env: []string{"COLOR=red", "EMPTY="}, Workdir: "/tmp", Interactive: true, Timeout: time.Second}
	got := e.Apply(base)
	if got.RootFS != base.RootFS || got.Hostname != base.Hostname || got.Memory != base.Memory || got.PidsLimit != base.PidsLimit || got.CPUQuota != base.CPUQuota || got.ReadOnly != base.ReadOnly || !reflect.DeepEqual(got.User, base.User) {
		t.Fatalf("container isolation changed: %+v", got)
	}
	if got.Workdir != "/tmp" || got.Timeout != time.Second || !got.Interactive || got.TTY || !reflect.DeepEqual(got.Command, e.Command) {
		t.Fatalf("execution overrides ignored: %+v", got)
	}
	wantEnv := []string{"PATH=/bin:/usr/bin", "HOME=/", "LANG=C", "COLOR=red", "BASE=kept", "EMPTY="}
	if !reflect.DeepEqual(got.CommandEnvironment(), wantEnv) {
		t.Fatalf("merged environment = %q; want %q", got.CommandEnvironment(), wantEnv)
	}
	got.Command[0] = "changed"
	got.Env[0] = "COLOR=changed"
	got.User.UID = 999
	if e.Command[0] != "echo" || base.Env[0] != "COLOR=blue" || base.User.UID != 123 {
		t.Fatal("applying execution aliased mutable input")
	}
	defaults := (Exec{Command: []string{"true"}}).Apply(base)
	if defaults.Workdir != base.Workdir || defaults.Timeout != 0 || defaults.Interactive || defaults.TTY {
		t.Fatalf("unexpected exec defaults: %+v", defaults)
	}
}
