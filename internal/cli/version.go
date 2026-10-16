package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"runtime"

	"github.com/mingo-liu/casklet/internal/buildinfo"
)

type engineVersionInfo struct {
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	SHA256 string `json:"sha256"`
}

type versionInfo struct {
	buildinfo.Info
	Engine      *engineVersionInfo `json:"engine,omitempty"`
	EngineError string             `json:"engine_error,omitempty"`
}

func parseVersion(r Request, args []string) (Request, error) {
	r.Action = "version"
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&r.JSON, "json", false, "print build information as JSON")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return parseFlagHelp("version", fs, args)
		}
		return r, err
	}
	if fs.NArg() != 0 {
		return r, errors.New("version takes no positional arguments")
	}
	return r, nil
}

func currentVersionInfo() versionInfo {
	info := versionInfo{Info: buildinfo.Current()}
	if hash, err := platformEngineSHA256(); err != nil {
		info.EngineError = err.Error()
	} else if hash != "" {
		info.Engine = &engineVersionInfo{OS: "linux", Arch: runtime.GOARCH, SHA256: hash}
	}
	return info
}

func executeVersion(r Request, stdout, stderr io.Writer) int {
	info := currentVersionInfo()
	var err error
	if r.JSON {
		err = json.NewEncoder(stdout).Encode(info)
	} else {
		modified := ""
		if info.Dirty {
			modified = " (modified)"
		}
		_, err = fmt.Fprintf(stdout, "casklet %s\nCommit: %s%s\nPlatform: %s/%s\nGo: %s\n", info.Version, info.Commit, modified, info.OS, info.Arch, info.GoVersion)
		if err == nil && info.Engine != nil {
			_, err = fmt.Fprintf(stdout, "Bundled engine: %s/%s sha256:%s\n", info.Engine.OS, info.Engine.Arch, info.Engine.SHA256)
		} else if err == nil && info.EngineError != "" {
			_, err = fmt.Fprintf(stdout, "Bundled engine: unavailable (%s)\n", info.EngineError)
		}
	}
	if err != nil {
		fmt.Fprintf(stderr, "casklet: write version information: %v\n", err)
		return 125
	}
	return 0
}
