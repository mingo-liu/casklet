//go:build darwin

package cli

import (
	"os"
	"reflect"
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
	if !strings.Contains(string(data), "mdocker doctor [--rootfs DIRECTORY]") {
		t.Fatalf("help omitted the default doctor template: %s", data)
	}
}

func TestMacHostArgumentErrorsDoNotRequireLima(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, tt := range []struct {
		args []string
		want string
	}{
		{[]string{"machine"}, "machine requires"},
		{[]string{"machine", "unknown"}, "unknown machine command"},
		{[]string{"machine", "init", "--unknown"}, "flag provided but not defined"},
		{[]string{"machine", "init", "unexpected"}, "unexpected machine init argument"},
		{[]string{"machine", "init", "--cpus", "0"}, "machine resources require"},
		{[]string{"machine", "start", "unexpected"}, "machine start takes no arguments"},
		{[]string{"machine", "stop", "unexpected"}, "machine stop takes no arguments"},
		{[]string{"machine", "status", "unexpected"}, "machine status takes no arguments"},
		{[]string{"rootfs"}, "rootfs requires one destination directory"},
		{[]string{"rootfs", ""}, "rootfs requires one destination directory"},
		{[]string{"rootfs", "one", "two"}, "rootfs requires one destination directory"},
	} {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			out, err := os.CreateTemp(t.TempDir(), "output")
			if err != nil {
				t.Fatal(err)
			}
			defer out.Close()
			if code := Execute(tt.args, os.Stdin, out, out); code != 125 {
				t.Fatalf("exit = %d; want 125", code)
			}
			data, err := os.ReadFile(out.Name())
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), tt.want) || strings.Contains(string(data), "Lima is required") {
				t.Fatalf("diagnostic = %q; want %q without Lima", data, tt.want)
			}
		})
	}
}

func TestMacMachineInitHelpAfterOptionsDoesNotRequireLima(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	out, err := os.CreateTemp(t.TempDir(), "output")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	if code := Execute([]string{"machine", "init", "--cpus", "2", "--help"}, os.Stdin, out, out); code != 0 {
		t.Fatalf("help exit = %d; want 0", code)
	}
}

func TestMacDefaultTemplateLeavesWorkloadFlagsAlone(t *testing.T) {
	for _, flag := range []string{"--image", "-image", "--image=literal", "-image=literal", "--rootfs", "-rootfs", "--rootfs=literal", "-rootfs=literal"} {
		t.Run(flag, func(t *testing.T) {
			command := []string{"echo", flag, "literal"}
			original := append([]string{"run", "--"}, command...)
			args := platformArguments(original)
			request, err := Parse(args)
			if err != nil || request.Config.RootFS != "builtin:busybox" || !reflect.DeepEqual(request.Config.Command, command) {
				t.Fatalf("default template: %+v %v", request, err)
			}
			if !reflect.DeepEqual(original, append([]string{"run", "--"}, command...)) {
				t.Fatalf("input arguments changed: %q", original)
			}
		})
	}
}

func TestMacExplicitTemplateAcceptsAllFlagSpellings(t *testing.T) {
	imageID := "sha256:" + strings.Repeat("a", 64)
	for _, source := range []struct{ name, value string }{{"rootfs", "/custom/rootfs"}, {"image", imageID}} {
		for _, prefix := range []string{"-", "--"} {
			for _, inline := range []bool{false, true} {
				flag := prefix + source.name
				options := []string{flag, source.value}
				if inline {
					options = []string{flag + "=" + source.value}
				}
				for _, action := range []string{"run", "doctor"} {
					if action == "doctor" && source.name == "image" {
						continue
					}
					original := append([]string{action}, options...)
					if action == "run" {
						original = append(original, "--", "true")
					}
					t.Run(strings.Join(original, " "), func(t *testing.T) {
						args := platformArguments(original)
						if !reflect.DeepEqual(args, original) {
							t.Fatalf("explicit source changed: %q; want %q", args, original)
						}
						request, err := Parse(args)
						if err != nil {
							t.Fatal(err)
						}
						if source.name == "rootfs" && request.Config.RootFS != source.value {
							t.Fatalf("rootfs = %q; want %q", request.Config.RootFS, source.value)
						}
						if source.name == "image" && (request.Config.Image != source.value || request.Config.RootFS != "") {
							t.Fatalf("image request = %+v", request.Config)
						}
					})
				}
			}
		}
	}
}

func TestMacDefaultTemplateScansOnlySourceOptions(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"image name", []string{"run", "--name", "image", "-d", "--", "true"}},
		{"rootfs name", []string{"run", "--name", "rootfs", "-d", "--", "true"}},
		{"image option value", []string{"run", "--name", "-image", "--", "true"}},
		{"rootfs option value", []string{"run", "--name", "--rootfs=literal", "--", "true"}},
		{"positional image", []string{"run", "image", "--", "true"}},
		{"source after positional", []string{"run", "unexpected", "--image", "literal", "--", "true"}},
		{"three dashes", []string{"run", "---image", "literal", "--", "true"}},
		{"dash positional", []string{"run", "-", "--image", "literal", "--", "true"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := append([]string{"run", "--rootfs", "builtin:busybox"}, tt.args[1:]...)
			if got := platformArguments(tt.args); !reflect.DeepEqual(got, want) {
				t.Fatalf("arguments = %q; want %q", got, want)
			}
		})
	}
}
