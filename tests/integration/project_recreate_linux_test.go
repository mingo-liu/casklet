//go:build linux

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	namepkg "github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/mingo-liu/casklet/internal/container"
	"github.com/mingo-liu/casklet/internal/image"
	"github.com/mingo-liu/casklet/internal/project"
)

func TestProjectSelectedRecreationPreviewAndFailureRecovery(t *testing.T) {
	require(t)
	t.Setenv("CASKLET_MACHINE_STATE_DIR", t.TempDir())
	source := template
	name := fmt.Sprintf("recreate-%x", time.Now().UnixNano())
	file := filepath.Join(t.TempDir(), "project.json")
	external := name + "-external"
	backgroundSuccess(t, "volume", "create", external)
	t.Cleanup(func() {
		backgroundCLI(t, "project", "down", "--volumes", name)
		backgroundCLI(t, "volume", "rm", external)
	})
	tcp, udp := freeNetworkPort(t, "tcp"), freeNetworkPort(t, "udp")
	dbRun := map[string]any{"rootfs": source, "command": []string{"/bin/integration-helper", "network-service"}, "env": []string{"VERSION=old"}, "mount": []string{"type=volume,source=data,target=/data", "type=volume,source=external,target=/external"}, "publish": []string{fmt.Sprintf("127.0.0.1:%d:8080", tcp), fmt.Sprintf("127.0.0.1:%d:7777/udp", udp)}}
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
	preview := backgroundSuccess(t, "project", "up", "--config", file, "--dry-run", "--json")
	var actions []project.Change
	if json.Unmarshal([]byte(preview), &actions) != nil || len(actions) != 4 {
		t.Fatalf("preview %s", preview)
	}
	if got := backgroundSuccess(t, "project", "ps", "--json", name); got != "[]\n" {
		t.Fatalf("preview created services: %s", got)
	}
	if projectRecreateCode(t, "volume", "inspect", "cp-"+name+"-data") != 125 || projectRecreateCode(t, "network", "inspect", "cp-"+name+"-default") != 125 {
		t.Fatal("preview created resources")
	}
	backgroundSuccess(t, "project", "up", "--config", file, "--wait", "--timeout", "30s")
	inspect := func(key string) container.Inspection {
		t.Helper()
		var value container.Inspection
		out := backgroundSuccess(t, "inspect", "cp-"+name+"-"+key)
		if err := json.Unmarshal([]byte(out), &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	db, api := inspect("db"), inspect("api")
	backgroundSuccess(t, "exec", db.ID, "--", "/bin/sh", "-c", "printf managed > /data/message; printf external > /external/message; touch /private-marker")
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
	// A drift in an unselected service fails without changing either execution.
	apiRun["env"] = []string{"DRIFT=yes"}
	save()
	if projectRecreateCode(t, "project", "up", "--config", file, "--recreate", "db") != 125 {
		t.Fatal("accepted unselected drift")
	}
	delete(apiRun, "env")
	dbRun["env"] = []string{"VERSION=new"}
	save()
	beforePreview := backgroundSuccess(t, "project", "ps", "--json", name)
	backgroundSuccess(t, "project", "up", "--config", file, "--recreate", "db", "--dry-run")
	if got := backgroundSuccess(t, "project", "ps", "--json", name); got != beforePreview {
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
	backgroundSuccess(t, "stop", "--timeout", "0s", api.ID)
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
	backgroundSuccess(t, "start", api.ID)
	api = inspect("api")
	// A separately owned host/container port must fail before removing old state.
	oldPublish := dbRun["publish"]
	conflict := startBackground(t, backgroundName(t), []string{"--network", "bridge", "--publish", fmt.Sprintf("127.0.0.1:%d:8080", freeNetworkPort(t, "tcp"))}, "/bin/sleep", "300")
	occupied := inspectBackground(t, conflict).Config.Publish[0]
	dbRun["publish"] = []string{fmt.Sprintf("127.0.0.1:%d:8080", occupied.HostPort)}
	save()
	if projectRecreateCode(t, "project", "up", "--config", file, "--recreate", "db") != 125 || inspect("db").ID != db.ID {
		t.Fatal("port conflict removed old service")
	}
	dbRun["publish"] = oldPublish
	save()
	backgroundSuccess(t, "project", "up", "--config", file, "--recreate", "db", "--wait", "--timeout", "30s")
	replaced, preserved := inspect("db"), inspect("api")
	if replaced.ID == db.ID || preserved.ID != api.ID || preserved.Generation != api.Generation {
		t.Fatalf("identities: db=%+v api=%+v", replaced, preserved)
	}
	if got := backgroundSuccess(t, "exec", replaced.ID, "--", "/bin/sh", "-c", `test ! -e /private-marker; printf '%s:' "$VERSION"; cat /data/message; printf ':'; cat /external/message`); got != "new:managed:external" {
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
	if json.Unmarshal([]byte(backgroundSuccess(t, "project", "ps", "--json", name)), &left) != nil || len(left) != 1 {
		t.Fatalf("partial failure records: %+v", left)
	}
	dbRun["command"] = []string{"/bin/integration-helper", "network-service"}
	save()
	backgroundSuccess(t, "project", "up", "--config", file, "--recreate", "db")
	if got := backgroundSuccess(t, "exec", "cp-"+name+"-db", "--", "/bin/sh", "-c", "cat /data/message; cat /external/message"); got != "managedexternal" {
		t.Fatalf("retry lost data %q", got)
	}
}

func projectRecreateCode(t *testing.T, args ...string) int {
	code, _, _ := backgroundCLI(t, args...)
	return code
}

func TestProjectRecreationUsesExplicitlyRefreshedMutableImageTag(t *testing.T) {
	require(t)
	t.Setenv("CASKLET_MACHINE_STATE_DIR", t.TempDir())
	reference, _ := ociRegistryFixture(t)
	name := fmt.Sprintf("image-recreate-%x", time.Now().UnixNano())
	file := filepath.Join(t.TempDir(), "project.json")
	document := map[string]any{"name": name, "networks": []string{}, "services": map[string]any{"app": map[string]any{"run": map[string]any{"image": reference, "entrypoint": "", "network": "none", "no-healthcheck": true, "command": []string{"/bin/sleep", "300"}}}}}
	data, _ := json.Marshal(document)
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { backgroundCLI(t, "project", "down", name) })
	// A cold preview must not pull or acquire an image.
	backgroundSuccess(t, "project", "up", "--config", file, "--dry-run")
	store, err := image.OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Resolve(context.Background(), reference); !errors.Is(err, image.ErrNotFound) {
		t.Fatalf("preview pulled image: %v", err)
	}
	backgroundSuccess(t, "project", "up", "--config", file)
	original := inspectBackground(t, "cp-"+name+"-app")
	oldID := original.Config.Image
	ref, err := namepkg.ParseReference(reference)
	if err != nil {
		t.Fatal(err)
	}
	img, err := remote.Image(ref)
	if err != nil {
		t.Fatal(err)
	}
	defaults, err := img.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	defaults.Config.Env = append(defaults.Config.Env, "IMAGE_VALUE=refreshed")
	updated, err := mutate.ConfigFile(img, defaults)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(ref, updated); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { backgroundCLI(t, "image", "rm", oldID) })
	backgroundSuccess(t, "project", "up", "--config", file, "--recreate", "app")
	cached := inspectBackground(t, "cp-"+name+"-app")
	if cached.Config.Image != oldID || cached.ID == original.ID {
		t.Fatalf("recreation implicitly refreshed cached tag: %+v", cached)
	}
	freshID := strings.TrimSpace(backgroundSuccess(t, "image", "pull", reference))
	t.Cleanup(func() { backgroundCLI(t, "image", "rm", freshID) })
	if freshID == oldID {
		t.Fatal("fixture did not refresh")
	}
	backgroundSuccess(t, "project", "up", "--config", file)
	if got := inspectBackground(t, "cp-"+name+"-app"); got.Config.Image != oldID || got.ID != cached.ID {
		t.Fatal("ordinary up replaced immutable cached execution")
	}
	backgroundSuccess(t, "project", "up", "--config", file, "--recreate", "app")
	refreshed := inspectBackground(t, "cp-"+name+"-app")
	if refreshed.Config.Image != freshID || refreshed.ID == cached.ID {
		t.Fatalf("recreate did not use refresh: %+v", refreshed)
	}
	if got := backgroundSuccess(t, "exec", refreshed.ID, "--", "/bin/sh", "-c", "printf '%s' \"$IMAGE_VALUE\""); got != "refreshed" {
		t.Fatalf("image defaults %q", got)
	}
	backgroundSuccess(t, "project", "down", name)
}
