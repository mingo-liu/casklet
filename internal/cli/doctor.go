package cli

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/mingo-liu/casklet/internal/buildinfo"
	"github.com/mingo-liu/casklet/internal/machine"
	containerruntime "github.com/mingo-liu/casklet/internal/runtime"
	"github.com/mingo-liu/casklet/internal/storage"
)

type doctorCheck struct {
	Name  string `json:"name"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}
type doctorReport struct {
	SchemaVersion int                  `json:"schema_version"`
	OK            bool                 `json:"ok"`
	Client        *versionInfo         `json:"client,omitempty"`
	Engine        *buildinfo.Info      `json:"engine,omitempty"`
	Machine       *machine.Diagnostics `json:"machine,omitempty"`
	Filesystem    *storage.Filesystem  `json:"filesystem,omitempty"`
	Checks        []doctorCheck        `json:"checks"`
}

func (report *doctorReport) check(name string, err error) {
	c := doctorCheck{Name: name, OK: err == nil}
	if err != nil {
		c.Error = err.Error()
	}
	report.Checks = append(report.Checks, c)
}
func writeDoctor(report doctorReport, asJSON bool, out, diagnostic io.Writer) int {
	report.SchemaVersion = 1
	report.OK = len(report.Checks) > 0
	for _, check := range report.Checks {
		report.OK = report.OK && check.OK
	}
	var err error
	if asJSON {
		err = json.NewEncoder(out).Encode(report)
	} else {
		write := func(format string, values ...any) {
			if err == nil {
				_, err = fmt.Fprintf(out, format, values...)
			}
		}
		if report.Client != nil {
			write("Client: %s (%s/%s, %s)\n", report.Client.Version, report.Client.OS, report.Client.Arch, report.Client.Commit)
		}
		if report.Engine != nil {
			write("Engine: %s (%s/%s, %s)\n", report.Engine.Version, report.Engine.OS, report.Engine.Arch, report.Engine.Commit)
		}
		if report.Machine != nil {
			write("Lima: %s\n", report.Machine.LimaVersion)
			if report.Machine.VM != nil {
				v := report.Machine.VM
				write("VM: %s %s (%s, cpus=%d, memory=%d bytes, disk=%d bytes configured)\n", v.Name, v.Status, v.Arch, v.CPUs, v.Memory, v.Disk)
			}
		}
		if report.Filesystem != nil {
			write("Guest data filesystem: %d bytes available / %d bytes total (low space: %t)\n", report.Filesystem.AvailableBytes, report.Filesystem.TotalBytes, report.Filesystem.LowSpace)
		}
		for _, check := range report.Checks {
			if check.OK {
				write("[ok] %s\n", check.Name)
			} else {
				write("[failed] %s: %s\n", check.Name, check.Error)
				fmt.Fprintf(diagnostic, "casklet: %s: %s\n", check.Name, check.Error)
			}
		}
		if report.OK {
			write("All required runtime capabilities are available.\n")
		}
	}
	if err != nil {
		fmt.Fprintf(diagnostic, "casklet: write doctor report: %v\n", err)
		return 125
	}
	if !report.OK {
		return 125
	}
	return 0
}
func engineDoctorReport() doctorReport {
	info := buildinfo.Current()
	return doctorReport{Engine: &info}
}

func executeEngineDoctor(r Request, out, diagnostic io.Writer) int {
	report := engineDoctorReport()
	report.check("runtime", containerruntime.Check(r.Config.RootFS))
	fs, err := doctorFilesystem()
	report.check("data_filesystem", err)
	if err == nil {
		report.Filesystem = &fs
	}
	return writeDoctor(report, r.JSON, out, diagnostic)
}
