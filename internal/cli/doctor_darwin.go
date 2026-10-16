//go:build darwin

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/mingo-liu/casklet/internal/machine"
)

func executeDarwinDoctor(ctx context.Context, args []string, r Request, stdin, stdout, stderr *os.File) int {
	info := currentVersionInfo()
	report := doctorReport{Client: &info}
	if info.EngineError != "" {
		report.check("bundled_engine", errors.New(info.EngineError))
	} else {
		report.check("bundled_engine", nil)
	}
	paths, err := hostPathArguments(args, r.Action)
	if err != nil {
		report.check("host_paths", err)
		return writeDoctor(report, r.JSON, stdout, stderr)
	}
	// Always capture one guest JSON object. Setup/install progress stays on stderr.
	paths = append(paths, "--json=true")
	file, err := os.CreateTemp("", "casklet-doctor-*")
	if err != nil {
		report.check("guest_report", err)
		return writeDoctor(report, r.JSON, stdout, stderr)
	}
	defer os.Remove(file.Name())
	defer file.Close()
	code, guestErr := machine.Execute(ctx, machine.Invocation{Args: paths}, stdin, file, stderr)
	if _, err = file.Seek(0, io.SeekStart); err == nil {
		var guest doctorReport
		decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
		err = decoder.Decode(&guest)
		if err == nil && (guest.SchemaVersion != 1 || guest.Engine == nil || len(guest.Checks) == 0) {
			err = errors.New("incomplete guest doctor report")
		}
		if err == nil {
			var extra any
			if decoder.Decode(&extra) != io.EOF {
				err = errors.New("invalid guest doctor report suffix")
			}
		}
		if err == nil {
			report.Engine, report.Filesystem = guest.Engine, guest.Filesystem
			report.Checks = append(report.Checks, guest.Checks...)
		}
	}
	if guestErr != nil {
		report.check("guest_transport", guestErr)
	} else if err != nil {
		report.check("guest_report", err)
	}
	// A nonzero guest must never become a successful report, even if its JSON
	// failed to include a corresponding check failure.
	if code != 0 && guestErr == nil && err == nil {
		report.check("guest_execution", fmt.Errorf("guest exited with status %d", code))
	}
	statusCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	status, statusErr := machine.Diagnose(statusCtx)
	report.Machine = &status
	report.check("machine", statusErr)
	return writeDoctor(report, r.JSON, stdout, stderr)
}
