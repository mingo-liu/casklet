//go:build darwin

package macos

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/mingo-liu/casklet/internal/container"
	"github.com/mingo-liu/casklet/internal/project"
)

func TestProjectSelectedRecreationPreviewAndFailureRecovery(t *testing.T) {
	client(t)
	success(t, "doctor")
	source := filepath.Join(hostDirectory(t), "template")
	success(t, "rootfs", source)
	build := exec.Command("go", "build", "-o", filepath.Join(source, "bin", "network-helper"), "../integration/testdata")
	build.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+runtime.GOARCH, "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("helper %v %s", err, out)
	}
	name := fmt.Sprintf("recreate-%x", time.Now().UnixNano())
	file := filepath.Join(hostDirectory(t), "project.json")
	external := name + "-external"
	success(t, "volume", "create", external)
	t.Cleanup(func() { command(t, "project", "down", "--volumes", name); command(t, "volume", "rm", external) })
	tcp, udp := freePort(t), freePort(t)
	dbRun := map[string]any{"rootfs": source, "command": []string{"/bin/network-helper", "network-service"}, "env": []string{"VERSION=old"}, "mount": []string{"type=volume,source=data,target=/data", "type=volume,source=external,target=/external"}, "publish": []string{fmt.Sprintf("127.0.0.1:%d:8080", tcp), fmt.Sprintf("127.0.0.1:%d:7777/udp", udp)}}
	apiBind := filepath.Join(filepath.Dir(file), "api-bind")
	if err := os.MkdirAll(apiBind, 0700); err != nil {
		t.Fatal(err)
	}
	apiRun := map[string]any{"mount": []string{"type=bind,source=" + apiBind + ",target=/bind"}, "rootfs": source, "command": []string{"/bin/sleep", "300"}}
	document := map[string]any{"name": name, "volumes": []string{"data"}, "external-volumes": map[string]string{"external": external}, "services": map[string]any{"db": map[string]any{"run": dbRun}, "api": map[string]any{"run": apiRun}}}
	save := func() {
		t.Helper()
		data, err := json.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	save()
	preview := success(t, "project", "up", "--config", file, "--dry-run", "--json")
	var actions []project.Change
	if json.Unmarshal([]byte(preview), &actions) != nil || len(actions) != 4 {
		t.Fatalf("preview %s", preview)
	}
	if got := success(t, "project", "ps", "--json", name); got != "[]\n" {
		t.Fatalf("preview created services: %s", got)
	}
	if projectRecreateCode(t, "volume", "inspect", "cp-"+name+"-data") != 125 || projectRecreateCode(t, "network", "inspect", "cp-"+name+"-default") != 125 {
		t.Fatal("preview created resources")
	}
	success(t, "project", "up", "--config", file, "--wait", "--timeout", "30s")
	inspect := func(key string) container.Inspection {
		t.Helper()
		var value container.Inspection
		out := success(t, "inspect", "cp-"+name+"-"+key)
		if err := json.Unmarshal([]byte(out), &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	db, api := inspect("db"), inspect("api")
	success(t, "exec", db.ID, "--", "/bin/sh", "-c", "printf managed > /data/message; printf external > /external/message; touch /private-marker")
	// The private lifecycle protocol rejects stale generations and ownership.
	if projectRecreateCode(t, "__project-replace", db.ID, fmt.Sprint(db.Generation+1), name, "db", db.Labels[project.LabelSpec]) != 125 {
		t.Fatal("replacement accepted stale generation")
	}
	if projectRecreateCode(t, "__project-replace", db.ID, fmt.Sprint(db.Generation), name, "db", "wrong-spec") != 125 {
		t.Fatal("replacement accepted wrong ownership")
	}
	assertPorts := func() {
		t.Helper()
		address := fmt.Sprintf("127.0.0.1:%d", tcp)
		deadline := time.Now().Add(10 * time.Second)
		available := false
		for time.Now().Before(deadline) {
			client := &http.Client{Timeout: 500 * time.Millisecond}
			response, err := client.Get("http://" + address)
			if err == nil {
				data, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
				response.Body.Close()
				if len(data) > 0 {
					available = true
					break
				}
			}
			time.Sleep(50 * time.Millisecond)
		}
		if !available {
			t.Fatal("recreated TCP publication unavailable")
		}
		connection, err := net.Dial("udp4", fmt.Sprintf("127.0.0.1:%d", udp))
		if err != nil {
			t.Fatal(err)
		}
		defer connection.Close()
		buffer := make([]byte, 64)
		for time.Now().Before(deadline) {
			connection.SetDeadline(time.Now().Add(300 * time.Millisecond))
			connection.Write([]byte("recreate"))
			n, err := connection.Read(buffer)
			if err == nil && string(buffer[:n]) == "echo:recreate" {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatal("recreated UDP publication unavailable")
	}
	assertPorts()
	// An occupied saved port of an unselected stopped service fails before deletion.
	success(t, "stop", "--timeout", "0s", db.ID)
	var held net.Listener
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		held, _ = net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", tcp))
		if held != nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if held == nil {
		t.Fatal("Lima did not release stopped port")
	}
	failedPort := projectRecreateCode(t, "project", "up", "--config", file, "--recreate", "api")
	held.Close()
	unchangedAPI := inspect("api")
	if failedPort != 125 || unchangedAPI.ID != api.ID || unchangedAPI.Generation != api.Generation || unchangedAPI.State != container.StateRunning || inspect("db").State != container.StateExited {
		t.Fatal("saved host port failure removed selected service")
	}
	success(t, "start", db.ID)
	db = inspect("db")
	// A drift in an unselected service fails without changing either execution.
	apiRun["env"] = []string{"DRIFT=yes"}
	save()
	if projectRecreateCode(t, "project", "up", "--config", file, "--recreate", "db") != 125 {
		t.Fatal("accepted unselected drift")
	}
	delete(apiRun, "env")
	dbRun["env"] = []string{"VERSION=new"}
	save()
	beforePreview := success(t, "project", "ps", "--json", name)
	success(t, "project", "up", "--config", file, "--recreate", "db", "--dry-run")
	if got := success(t, "project", "ps", "--json", name); got != beforePreview {
		t.Fatalf("preview changed executions: %s", got)
	}
	// Missing bind sources must fail before removing the old container.
	mounts := dbRun["mount"]
	dbRun["mount"] = append(append([]string(nil), mounts.([]string)...), "type=bind,source="+filepath.Join(filepath.Dir(file), "missing")+",target=/missing")
	save()
	if projectRecreateCode(t, "project", "up", "--config", file, "--recreate", "db") != 125 || inspect("db").ID != db.ID {
		t.Fatal("invalid source removed old service")
	}
	dbRun["mount"] = mounts
	save()
	// Stopped unselected services validate their actual retained sources before removal.
	success(t, "stop", "--timeout", "0s", api.ID)
	if err := os.Rename(apiBind, apiBind+"-moved"); err != nil {
		t.Fatal(err)
	}
	failed := projectRecreateCode(t, "project", "up", "--config", file, "--recreate", "db")
	if err := os.Rename(apiBind+"-moved", apiBind); err != nil {
		t.Fatal(err)
	}
	if failed != 125 || inspect("db").ID != db.ID || inspect("api").State != container.StateExited {
		t.Fatal("retained-source failure removed selected service or resumed invalid source")
	}
	success(t, "start", api.ID)
	api = inspect("api")
	// A separately owned host/container port must fail before removing old state.
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	oldPublish := dbRun["publish"]
	dbRun["publish"] = []string{fmt.Sprintf("127.0.0.1:%d:8080", listener.Addr().(*net.TCPAddr).Port)}
	save()
	code := projectRecreateCode(t, "project", "up", "--config", file, "--recreate", "db")
	listener.Close()
	if code != 125 || inspect("db").ID != db.ID {
		t.Fatal("host port conflict removed old service")
	}
	dbRun["publish"] = oldPublish
	save()
	success(t, "project", "up", "--config", file, "--recreate", "db", "--wait", "--timeout", "30s")
	replaced, preserved := inspect("db"), inspect("api")
	if replaced.ID == db.ID || preserved.ID != api.ID || preserved.Generation != api.Generation {
		t.Fatalf("identities: db=%+v api=%+v", replaced, preserved)
	}
	if got := success(t, "exec", replaced.ID, "--", "/bin/sh", "-c", `test ! -e /private-marker; printf '%s:' "$VERSION"; cat /data/message; printf ':'; cat /external/message`); got != "new:managed:external" {
		t.Fatalf("persistent/private state %q", got)
	}
	assertPorts()
	// Rootfs command failure is a bounded partial replacement, not restoration.
	dbRun["command"] = []string{"/bin/does-not-exist"}
	save()
	if projectRecreateCode(t, "project", "up", "--config", file, "--recreate", "db") != 125 {
		t.Fatal("accepted invalid command")
	}
	if inspect("api").ID != api.ID {
		t.Fatal("failure changed unaffected service")
	}
	var left []container.Record
	if json.Unmarshal([]byte(success(t, "project", "ps", "--json", name)), &left) != nil || len(left) != 1 {
		t.Fatalf("partial failure records: %+v", left)
	}
	dbRun["command"] = []string{"/bin/network-helper", "network-service"}
	save()
	success(t, "project", "up", "--config", file, "--recreate", "db")
	if got := success(t, "exec", "cp-"+name+"-db", "--", "/bin/sh", "-c", "cat /data/message; cat /external/message"); got != "managedexternal" {
		t.Fatalf("retry lost data %q", got)
	}
}

func projectRecreateCode(t *testing.T, args ...string) int {
	_, _, code := command(t, args...)
	return code
}
