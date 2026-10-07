package cli

import (
	"os"
	"testing"
)

func TestSecurityOptions(t *testing.T) {
	request, err := Parse([]string{"run", "--rootfs", "/template", "--userns", "--uid-map", "0:200000:1000", "--uid-map", "1000:400000:1000", "--gid-map", "0:300000:2000", "--user", "1000", "--seccomp", "unconfined", "--", "/bin/id"})
	if err != nil {
		t.Fatal(err)
	}
	if !request.Config.UserNS || request.Config.SeccompProfile() != "unconfined" || len(request.Config.UIDMappings) != 2 {
		t.Fatalf("config = %+v", request.Config)
	}
	request, err = Parse([]string{"run", "--rootfs", "/template", "--rootless", "--", "/bin/id"})
	if err != nil {
		t.Fatal(err)
	}
	if !request.Config.UserNS || request.Config.UIDMappings[0].HostID != uint32(os.Getuid()) || request.Config.GIDMappings[0].HostID != uint32(os.Getgid()) {
		t.Fatalf("rootless config = %+v", request.Config)
	}
	for _, options := range [][]string{
		{"--seccomp", ""}, {"--seccomp", "custom"}, {"--userns"}, {"--uid-map", "0:200000:1000"},
		{"--rootless", "-d"}, {"--rootless", "--network", "bridge"}, {"--rootless", "--user", "1"},
		{"--userns", "--uid-map", "0:200000:1000", "--gid-map", "0:300000:1000", "-d"},
	} {
		args := append([]string{"run", "--rootfs", "/template"}, options...)
		args = append(args, "--", "/bin/id")
		if _, err := Parse(args); err == nil {
			t.Fatalf("accepted %v", options)
		}
	}
}
