//go:build darwin

package cli

import (
	"os"
	"strings"
	"testing"
)

func TestMacHelpAndInvalidArgumentsDoNotStartMachine(t *testing.T) {
	out, err := os.CreateTemp(t.TempDir(), "output")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	t.Setenv("PATH", t.TempDir())
	if code := Execute([]string{"help"}, os.Stdin, out, out); code != 0 {
		t.Fatalf("help requires Lima: %d", code)
	}
	if code := Execute([]string{"run", "--unknown", "--", "true"}, os.Stdin, out, out); code != 125 {
		t.Fatalf("invalid arguments: %d", code)
	}
	data, err := os.ReadFile(out.Name())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "Lima is required") {
		t.Fatalf("attempted machine setup: %s", data)
	}
}

func TestMacDefaultTemplateLeavesWorkloadFlagsAlone(t *testing.T) {
	args := platformArguments([]string{"run", "--", "echo", "--image", "literal"})
	request, err := Parse(args)
	if err != nil || request.Config.RootFS != "builtin:busybox" || len(request.Config.Command) != 3 {
		t.Fatalf("default template: %+v %v", request, err)
	}
	args = platformArguments([]string{"run", "--image", "sha256:" + strings.Repeat("a", 64), "--", "true"})
	request, err = Parse(args)
	if err != nil || request.Config.RootFS != "" {
		t.Fatalf("image changed: %+v %v", request, err)
	}
}
