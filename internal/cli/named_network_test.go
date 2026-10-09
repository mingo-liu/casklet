package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestNamedNetworkCommandsAndRunConfig(t *testing.T) {
	for _, args := range [][]string{{"network", "create", "demo"}, {"network", "inspect", "demo"}, {"network", "rm", "demo"}, {"network", "ls", "--json"}} {
		r, err := Parse(args)
		if err != nil || r.Action != "network-"+args[1] {
			t.Fatalf("%v: %+v %v", args, r, err)
		}
		host, err := hostPathArguments(args, r.Action)
		if err != nil || !reflect.DeepEqual(host, args) {
			t.Fatalf("network host arguments changed %v", host)
		}
	}
	for _, args := range [][]string{{"network", "create", "none"}, {"network", "create", "bad_name"}, {"network", "create"}, {"network", "ls", "demo"}, {"network", "unknown"}, {"run", "-d", "--rootfs", "/tmp/root", "--network", "demo", "--name", "Bad_Name", "--", "true"}, {"run", "--rootfs", "/tmp/root", "--network", "demo", "--network-alias", "redis", "--", "true"}} {
		if _, err := Parse(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	r, err := Parse([]string{"run", "-d", "--rootfs", "/tmp/root", "--network", "demo", "--network-alias", "redis", "--name", "demo-redis", "--", "true"})
	if err != nil || !reflect.DeepEqual(r.Config.NetworkAliases, []string{"redis"}) {
		t.Fatalf("run: %+v %v", r, err)
	}
}

func TestNamedNetworkJSONConfigAndAliasMerge(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "project.json")
	if err := os.WriteFile(filename, []byte(`{"rootfs":"template","detach":true,"name":"project-service","network":"demo","network-alias":["redis"],"command":["true"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := Parse([]string{"run", "--config", filename, "--network-alias", "cache"})
	if err != nil || r.Config.Network != "demo" || !reflect.DeepEqual(r.Config.NetworkAliases, []string{"redis", "cache"}) {
		t.Fatalf("config aliases: %+v %v", r, err)
	}
}
