package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRunConfigAndEnvironmentPrecedence(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write("base.env", "# ignored\r\n\r\nVALUE=file\r\nEMPTY=\r\nLITERAL=\"$HOME # unchanged\"\r\n")
	extra := write("extra.env", "VALUE=extra\n")
	filename := write("run.json", `{"rootfs":"template","detach":true,"name":"saved","memory":"64m","env-file":["base.env"],"env":["VALUE=config"],"command":["/bin/echo","--help"]}`)
	r, err := Parse([]string{"run", "--config", filename, "--name", "override", "--memory", "256m", "--env", "VALUE=cli", "--env-file", extra})
	if err != nil {
		t.Fatal(err)
	}
	if r.Name != "override" || r.Config.Memory != 256<<20 || r.Config.RootFS != filepath.Join(dir, "template") || !r.Detach || !reflect.DeepEqual(r.Config.Command, []string{"/bin/echo", "--help"}) {
		t.Fatalf("config: %+v", r)
	}
	want := []string{"EMPTY=", "LITERAL=\"$HOME # unchanged\""}
	env := r.Config.CommandEnvironment()
	for _, v := range want {
		if !strings.Contains(strings.Join(env, "\n"), v) {
			t.Fatalf("env: %q", env)
		}
	}
	if env[3] != "VALUE=cli" {
		t.Fatalf("precedence: %q", env)
	}
	filename = write("override.json", `{"rootfs":"template","detach":true,"command":["true"]}`)
	expanded, err := expandFileArguments([]string{"run", "--config", filename, "--rootfs", "/override", "-d=false", "--", "/bin/true"})
	if err != nil {
		t.Fatal(err)
	}
	r, err = Parse(expanded)
	if err != nil {
		t.Fatal(err)
	}
	if r.Detach || r.Config.RootFS != "/override" || !reflect.DeepEqual(r.Config.Command, []string{"/bin/true"}) {
		t.Fatalf("override: %+v", r)
	}
	for _, v := range expanded {
		if strings.HasPrefix(v, "--config") || strings.HasPrefix(v, "--env-file") {
			t.Fatalf("host file leaked into guest args: %q", expanded)
		}
	}
}

func TestOptionFilesRejectInvalidInputsWithoutLeakingValues(t *testing.T) {
	dir := t.TempDir()
	filename := filepath.Join(dir, "run.json")
	for _, body := range []string{`[]`, `null`, `{"unknown":true}`, `{"memory":128}`, `{"env":"A=B"}`, `{"detach":"true"}`, `{"command":[]}`, `{"command":null}`, `{"env":null}`, `{"image":"a","image":"b"}`, `{} {}`} {
		if err := os.WriteFile(filename, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Parse([]string{"run", "--config", filename, "--rootfs", "/template", "--", "true"}); err == nil {
			t.Errorf("accepted %s", body)
		}
	}
	envfile := filepath.Join(dir, "secrets.env")
	os.WriteFile(envfile, []byte("BAD-KEY=private-secret\n"), 0600)
	_, err := Parse([]string{"exec", "--env-file", envfile, "worker", "--", "true"})
	if err == nil || strings.Contains(err.Error(), "private-secret") {
		t.Fatalf("invalid env: %v", err)
	}
	for _, args := range [][]string{{"run", "--config", filename, "--config", filename, "--", "true"}, {"run", "--env-file", dir, "--", "true"}} {
		if _, err := Parse(args); err == nil {
			t.Fatalf("accepted %q", args)
		}
	}
}

func TestOptionFileHelpAndLiteralArguments(t *testing.T) {
	for _, args := range [][]string{{"run", "--config", "/missing", "--help"}, {"run", "--env-file", "/missing", "--help"}, {"exec", "--env-file", "/missing", "--help"}} {
		r, err := Parse(args)
		if err != nil || r.Action != "help" {
			t.Fatalf("help reads files: %+v %v", r, err)
		}
	}
	args := []string{"run", "--rootfs", "/template", "--env", "VALUE=--config", "--", "echo", "--env-file", "/missing"}
	r, err := Parse(args)
	if err != nil || !reflect.DeepEqual(r.Config.Command, args[6:]) {
		t.Fatalf("literals: %+v %v", r, err)
	}
}
