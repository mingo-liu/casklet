package machine

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func sharedInstance(t *testing.T) (Instance, string) {
	t.Helper()
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var instance Instance
	data := `{"config":{"mounts":[{"location":` + quoteJSON(directory) + `,"mountPoint":"/mnt/host","writable":true}]}}`
	if err := json.Unmarshal([]byte(data), &instance); err != nil {
		t.Fatal(err)
	}
	return instance, directory
}

func quoteJSON(value string) string { data, _ := json.Marshal(value); return string(data) }

func TestGuestArgumentsMapOnlyHostResources(t *testing.T) {
	instance, directory := sharedInstance(t)
	args := []string{"run", "--rootfs", BuiltinRootFS, "--env", "PATH=/custom", "--mount=type=bind,source=" + directory + ",target=/data,readonly", "-p", "127.0.0.1:32123:80", "--publish=32124:53/udp", "--", "sh", "--mount", "literal", "a'b\n$(touch forbidden)"}
	got, err := guestArguments(instance, args)
	if err != nil {
		t.Fatal(err)
	}
	want := append([]string(nil), args...)
	want[2] = guestRootFS
	want[5] = "--mount=type=bind,source=/mnt/host,target=/data,readonly"
	want[7] = "127.0.0.3:32123:80/tcp"
	want[8] = "--publish=127.0.0.2:32124:53/udp"
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q; want %q", got, want)
	}
	if !reflect.DeepEqual(args[10:], got[10:]) {
		t.Fatal("workload changed")
	}
}

func TestHostPathRejectsUnsharedAndSymlinkEscapes(t *testing.T) {
	instance, directory := sharedInstance(t)
	outside := t.TempDir()
	link := filepath.Join(directory, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{outside, link, filepath.Join(directory, "missing")} {
		if got, err := instance.hostPath(path); err == nil {
			t.Fatalf("accepted %s as %s", path, got)
		}
	}
	instance.Config.Mounts[0].Writable = false
	if _, err := instance.hostPath(directory); err == nil {
		t.Fatal("accepted readonly runtime share")
	}
}

func TestRemoteCommandPreservesShellMetacharacters(t *testing.T) {
	values := []string{"spaces here", "a'b", "$HOME", "$(printf unexpected)", "`printf unexpected`", "line\nbreak", "", "--flag"}
	args := append([]string{"printf", "%s\\000"}, values...)
	output, err := exec.Command("/bin/sh", "-c", remoteCommand(args)).Output()
	if err != nil {
		t.Fatal(err)
	}
	if string(output) != strings.Join(values, "\x00")+"\x00" {
		t.Fatalf("remote shell changed data: %q", output)
	}
}

func TestPortPreflightDetectsTCPAndUDPConflicts(t *testing.T) {
	tcp, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()
	udp, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	for _, mapping := range []string{strconv.Itoa(tcp.Addr().(*net.TCPAddr).Port) + ":80/tcp", strconv.Itoa(udp.LocalAddr().(*net.UDPAddr).Port) + ":53/udp"} {
		if err := checkPorts([]string{"run", "-p", "127.0.0.1:" + mapping, "--", "true"}); err == nil {
			t.Fatal("accepted occupied host port")
		}
	}
	if err := checkPorts([]string{"run", "--env", "VALUE=-p", "--", "echo", "-p", "invalid"}); err != nil {
		t.Fatal(err)
	}
}

func TestMachineDiscoveryDoesNotAdoptDevelopmentVM(t *testing.T) {
	stub := filepath.Join(t.TempDir(), "limactl")
	script := "#!/bin/sh\nprintf '%s\\n' '{\"name\":\"mini-docker\",\"status\":\"Running\"}'\n"
	if err := os.WriteFile(stub, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	m := &Machine{lima: stub, stderr: io.Discard}
	instance, err := m.instance(context.Background())
	if err != nil || instance != nil {
		t.Fatalf("adopted development VM: %+v, %v", instance, err)
	}
	script += "printf '%s\\n' '{\"name\":\"mini-docker-runtime\",\"status\":\"Stopped\"}'\n"
	if err := os.WriteFile(stub, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	instance, err = m.instance(context.Background())
	if err != nil || instance == nil || instance.Status != "Stopped" {
		t.Fatalf("discovery: %+v, %v", instance, err)
	}
}
