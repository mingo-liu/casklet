package machine

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestInspectionPresentsHostResourcesAndPreservesGuestAndUnknownFields(t *testing.T) {
	instance, directory := sharedInstance(t)
	data := []byte(`{
		"id":"id", "future_record":{"enabled":true},
		"config":{
			"rootfs":"/mnt/host/template", "future_config":[1,2],
			"mounts":[{"source":"/mnt/host/data","target":"/data","read_only":true,"future_mount":"keep"}],
			"publish":[
				{"host_ip":"127.0.0.3","host_port":8080,"container_port":80,"protocol":"tcp","future_port":4},
				{"host_ip":"127.0.0.2","host_port":5353,"container_port":53,"protocol":"udp"},
				{"host_ip":"192.0.2.1","host_port":9090,"container_port":90,"protocol":"tcp"}
			]
		}
	}`)
	var output bytes.Buffer
	if err := presentInspection(instance, data, &output); err != nil {
		t.Fatal(err)
	}
	var original, got map[string]json.RawMessage
	if err := json.Unmarshal(data, &original); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(output.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	var originalConfig, hostConfig, guestResources map[string]json.RawMessage
	for _, decode := range []struct {
		data json.RawMessage
		out  *map[string]json.RawMessage
	}{{original["config"], &originalConfig}, {got["config"], &hostConfig}, {got["guest_resources"], &guestResources}} {
		if err := json.Unmarshal(decode.data, decode.out); err != nil {
			t.Fatal(err)
		}
	}
	for _, field := range []string{"rootfs", "mounts", "publish"} {
		var before, after any
		_ = json.Unmarshal(originalConfig[field], &before)
		_ = json.Unmarshal(guestResources[field], &after)
		if !reflect.DeepEqual(before, after) {
			t.Fatalf("guest %s changed: %s", field, guestResources[field])
		}
	}
	rootfs, _ := inspectionString(hostConfig["rootfs"])
	if rootfs != filepath.Join(directory, "template") {
		t.Fatalf("host rootfs = %q", rootfs)
	}
	var mounts []map[string]any
	var ports []map[string]any
	_ = json.Unmarshal(hostConfig["mounts"], &mounts)
	_ = json.Unmarshal(hostConfig["publish"], &ports)
	if mounts[0]["source"] != filepath.Join(directory, "data") || mounts[0]["target"] != "/data" || mounts[0]["read_only"] != true || mounts[0]["future_mount"] != "keep" {
		t.Fatalf("host mounts = %+v", mounts)
	}
	for i, want := range []string{"127.0.0.1", "0.0.0.0", "192.0.2.1"} {
		if ports[i]["host_ip"] != want {
			t.Fatalf("host port %d = %+v", i, ports[i])
		}
	}
	var futureRecord map[string]bool
	var futureConfig []int
	_ = json.Unmarshal(got["future_record"], &futureRecord)
	_ = json.Unmarshal(hostConfig["future_config"], &futureConfig)
	if ports[0]["future_port"] != float64(4) || ports[1]["protocol"] != "udp" || !futureRecord["enabled"] || !reflect.DeepEqual(futureConfig, []int{1, 2}) {
		t.Fatalf("unknown fields or port configuration changed: %s", output.Bytes())
	}
}

func TestMacPathUsesBoundariesAndMostSpecificShareWithoutFilesystemAccess(t *testing.T) {
	instance, directory := sharedInstance(t)
	var nested Instance
	if err := json.Unmarshal([]byte(`{"config":{"mounts":[
		{"location":"/missing/mac","mountPoint":"/mnt/host/nested"},
		{"location":`+quoteJSON(directory)+`,"mountPoint":"/mnt/host"},
		{"location":"/missing/same"}
	]}}`), &nested); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(directory); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ guest, host string }{
		{guestRootFS, BuiltinRootFS},
		{"", ""},
		{"/mnt/host/template", filepath.Join(directory, "template")},
		{"/mnt/host/nested/data", "/missing/mac/data"},
		{"/mnt/host-other/data", "/mnt/host-other/data"},
		{"/missing/same/data", "/missing/same/data"},
		{"/unknown/template", "/unknown/template"},
	} {
		if got := nested.macPath(test.guest); got != test.host {
			t.Errorf("macPath(%q) = %q; want %q", test.guest, got, test.host)
		}
	}
	// Inspection never requires the original rootfs or bind source to still exist.
	var out bytes.Buffer
	if err := presentInspection(instance, []byte(`{"config":{"rootfs":"/mnt/host/template","mounts":[{"source":"/mnt/host/data"}],"publish":[]}}`), &out); err != nil {
		t.Fatal(err)
	}
}

func TestInspectionBuiltinAndImageSources(t *testing.T) {
	for _, rootfs := range []string{guestRootFS, ""} {
		var output bytes.Buffer
		data := []byte(`{"config":{"rootfs":` + quoteJSON(rootfs) + `,"image":"sha256:identity","mounts":[],"publish":[]}}`)
		if err := presentInspection(Instance{}, data, &output); err != nil {
			t.Fatal(err)
		}
		want := rootfs
		if rootfs == guestRootFS {
			want = BuiltinRootFS
		}
		var decoded struct {
			Config struct{ RootFS, Image string }
		}
		if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded.Config.RootFS != want || decoded.Config.Image != "sha256:identity" {
			t.Fatalf("unexpected source: %+v", decoded.Config)
		}
	}
}

func TestInspectionRejectsMalformedSuccessfulResponsesWithoutPartialOutput(t *testing.T) {
	for _, data := range []string{
		`not JSON`, `null`, `[]`, `{}`, `{"config":null}`,
		`{"config":{"rootfs":"/rootfs","mounts":[]}}`,
		`{"config":{"rootfs":null,"mounts":[],"publish":[]}}`,
		`{"config":{"rootfs":"/rootfs","mounts":{},"publish":[]}}`,
		`{"config":{"rootfs":"/rootfs","mounts":[null],"publish":[]}}`,
		`{"config":{"rootfs":"/rootfs","mounts":[],"publish":[{"host_ip":5}]}}`,
		`{"config":{"rootfs":"/rootfs","mounts":[],"publish":[]}} {}`,
	} {
		var out bytes.Buffer
		inspection := &inspectionOutput{}
		_, _ = inspection.Write([]byte(data))
		code, err := guestResult(Instance{}, inspection, &out, nil)
		if code != 125 || err == nil || !strings.Contains(err.Error(), "invalid guest inspection") || out.Len() != 0 {
			t.Errorf("response %s: code=%d err=%v output=%q", data, code, err, out.String())
		}
	}
}

func TestInspectionBoundAndGuestExitStatus(t *testing.T) {
	inspection := &inspectionOutput{}
	data := bytes.Repeat([]byte{'x'}, maxInspectionBytes+1)
	if n, err := inspection.Write(data); n != len(data) || err != nil {
		t.Fatalf("bounded writer did not consume data: %d, %v", n, err)
	}
	if n, err := inspection.Write([]byte("more")); n != 4 || err != nil || inspection.data.Len() != maxInspectionBytes {
		t.Fatalf("bounded writer grew: %d, %v, size=%d", n, err, inspection.data.Len())
	}
	var output bytes.Buffer
	if code, err := guestResult(Instance{}, inspection, &output, nil); code != 125 || err == nil || output.Len() != 0 {
		t.Fatalf("oversized success: %d, %v, %q", code, err, output.String())
	}
	guestErr := exec.Command("/bin/sh", "-c", "exit 17").Run()
	if code, err := guestResult(Instance{}, inspection, &output, guestErr); code != 17 || err != nil || output.Len() != 0 {
		t.Fatalf("guest failure lost exit status: %d, %v, %q", code, err, output.String())
	}
	transportErr := errors.New("transport failed")
	if code, err := guestResult(Instance{}, inspection, &output, transportErr); code != 125 || !errors.Is(err, transportErr) {
		t.Fatalf("transport failure changed: %d, %v", code, err)
	}
	if code, err := guestResult(Instance{}, nil, &output, nil); code != 0 || err != nil || output.Len() != 0 {
		t.Fatalf("streaming result changed: %d, %v", code, err)
	}
}

type failedInspectionWriter struct{ err error }

func (writer failedInspectionWriter) Write([]byte) (int, error) { return 0, writer.err }

func TestInspectionReportsOutputFailure(t *testing.T) {
	inspection := &inspectionOutput{}
	_, _ = inspection.Write([]byte(`{"config":{"rootfs":"","mounts":[],"publish":[]}}`))
	for _, failure := range []error{errors.New("closed output"), nil} {
		want := failure
		if want == nil {
			want = io.ErrShortWrite
		}
		code, err := guestResult(Instance{}, inspection, failedInspectionWriter{failure}, nil)
		if code != 125 || !errors.Is(err, want) {
			t.Fatalf("output failure lost: %d, %v", code, err)
		}
	}
}
