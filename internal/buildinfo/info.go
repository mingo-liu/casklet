// Package buildinfo reports local build metadata without starting the runtime.
package buildinfo

import (
	"runtime"
	"runtime/debug"
)

// Set identically for the client and its embedded engine by make build.
var version = "dev"

type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	Dirty     bool   `json:"dirty"`
	GoVersion string `json:"go_version"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
}

func Current() Info {
	i := Info{Version: version, Commit: "unknown", GoVersion: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH}
	if i.Version == "" {
		i.Version = "dev"
	}
	if build, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range build.Settings {
			switch setting.Key {
			case "vcs.revision":
				i.Commit = setting.Value
			case "vcs.modified":
				i.Dirty = setting.Value == "true"
			}
		}
	}
	return i
}
