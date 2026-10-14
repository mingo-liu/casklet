package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunTmpfsCapacitiesAndInvalidInputs(t *testing.T) {
	for _, flag := range []string{"--tmp-size", "--shm-size"} {
		for _, value := range []string{"", "0", "0m", "-1", "-2m", "1.5g", "1t", "137438953473", "129g", "9223372036854775807g"} {
			if _, err := Parse([]string{"run", "--rootfs", "/template", flag, value, "--", "true"}); err == nil {
				t.Fatalf("accepted %s %q", flag, value)
			}
		}
	}
	r, err := Parse([]string{"run", "--rootfs", "/template", "--tmp-size=32M", "--shm-size", "128g", "--", "echo", "--tmp-size", "0"})
	if err != nil || r.Config.TmpSize != 32<<20 || r.Config.ShmSize != 128<<30 || strings.Join(r.Config.Command, " ") != "echo --tmp-size 0" {
		t.Fatalf("capacities or separator: %+v %v", r, err)
	}
	r, err = Parse([]string{"run", "--rootfs", "/template", "--", "true"})
	if err != nil || r.Config.TmpSize != 0 || r.Config.ShmSize != 0 {
		t.Fatalf("legacy default fields: %+v %v", r, err)
	}
	for _, flag := range []string{"--tmp-size", "--shm-size"} {
		if _, err := Parse([]string{"exec", flag, "32m", "worker", "--", "true"}); err == nil {
			t.Fatalf("exec accepted %s", flag)
		}
	}
}

func TestRunFileTmpfsCapacityPrecedenceAndTypes(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "run.json")
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(filename, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"rootfs":"template","tmp-size":"32m","shm-size":"96m","command":["true"]}`)
	r, err := Parse([]string{"run", "--config", filename, "--tmp-size", "48m"})
	if err != nil || r.Config.TmpSize != 48<<20 || r.Config.ShmSize != 96<<20 {
		t.Fatalf("precedence %+v %v", r, err)
	}
	for _, body := range []string{`{"tmp-size":32}`, `{"shm-size":null}`, `{"tmp-size":"0"}`, `{"shm-size":"129g"}`, `{"tmp-size":"1m","tmp-size":"2m"}`} {
		write(body)
		if _, err := Parse([]string{"run", "--config", filename, "--rootfs", "/template", "--", "true"}); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
}

func TestProjectTmpfsCapacitiesAffectIdentity(t *testing.T) {
	plan := func(sizes string) Request {
		t.Helper()
		filename := projectConfigFile(t, `{"name":"demo","services":{"app":{"run":{"image":"app:1"`+sizes+`}}}}`)
		r, err := Parse([]string{"project", "up", "--config", filename})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	first := plan(`,"tmp-size":"32m","shm-size":"96m"`)
	service := first.Project.Services[0]
	r, err := Parse(service.Args)
	if err != nil || r.Config.TmpSize != 32<<20 || r.Config.ShmSize != 96<<20 {
		t.Fatalf("project capacities %+v %v", r, err)
	}
	if other := plan(`,"shm-size":"98304k","tmp-size":"33554432"`); other.Project.Services[0].Fingerprint != service.Fingerprint {
		t.Fatal("equivalent byte capacities changed identity")
	}
	for _, sizes := range []string{``, `,"tmp-size":"48m","shm-size":"96m"`, `,"tmp-size":"32m","shm-size":"64m"`} {
		if other := plan(sizes); other.Project.Services[0].Fingerprint == service.Fingerprint {
			t.Fatal("mount capacity drift went undetected")
		}
	}
}
