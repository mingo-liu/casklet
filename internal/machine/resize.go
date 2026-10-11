package machine

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"runtime"
	"strconv"
)

const gib = int64(1 << 30)

func parseResize(args []string) (Options, error) {
	var options Options
	fs := flag.NewFlagSet("machine resize", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.IntVar(&options.CPUs, "cpus", 0, "VM CPU count")
	fs.IntVar(&options.Memory, "memory", 0, "VM memory in GiB")
	fs.IntVar(&options.Disk, "disk", 0, "VM disk in GiB")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return options, resourceHelpResult("resize", args)
		}
		return options, err
	}
	if fs.NArg() != 0 {
		return options, errors.New("unexpected machine resize argument")
	}
	var invalid bool
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "cpus":
			invalid = invalid || options.CPUs < 1 || options.CPUs > 64
		case "memory":
			invalid = invalid || options.Memory < 1 || options.Memory > 128
		case "disk":
			invalid = invalid || options.Disk < 4 || options.Disk > 1024
		}
	})
	if invalid {
		return options, errors.New("machine resources require 1-64 CPUs, 1-128 GiB memory, and 4-1024 GiB disk")
	}
	return options, validateResize(options)
}

func validateResize(options Options) error {
	if options.CPUs == 0 && options.Memory == 0 && options.Disk == 0 {
		return errors.New("machine resize requires at least one of --cpus, --memory, or --disk")
	}
	if options.CPUs < 0 || options.CPUs > 64 || options.Memory < 0 || options.Memory > 128 || options.Disk < 0 || options.Disk > 1024 || (options.Disk > 0 && options.Disk < 4) {
		return errors.New("machine resources require 1-64 CPUs, 1-128 GiB memory, and 4-1024 GiB disk")
	}
	return nil
}

// resize delegates edits to Lima so shares, provisioning, forwarding, and the
// existing disk remain intact. Lima applies growth on the next VM start.
func (m *Machine) resize(ctx context.Context, options Options, stdout io.Writer) error {
	if err := validateResize(options); err != nil {
		return err
	}
	lock, err := m.lock(ctx)
	if err != nil {
		return err
	}
	defer lock.Close()
	instance, err := m.instance(ctx)
	if err != nil {
		return err
	}
	if instance == nil {
		return errors.New("runtime machine is not initialized; create it with casklet machine init")
	}
	if instance.Status != "Stopped" {
		return errors.New("stop the runtime machine before resizing: casklet machine stop (terminates running containers and preserves their files); then retry machine resize and run casklet machine start")
	}
	if instance.VMType != "vz" || instance.Arch != map[string]string{"arm64": "aarch64", "amd64": "x86_64"}[runtime.GOARCH] || instance.Config.OS != "Linux" {
		return errors.New("runtime machine must be a native-architecture Linux VZ machine before resizing")
	}
	if instance.CPUs < 1 || instance.Memory < 1 || instance.Disk < 1 {
		return errors.New("runtime machine resource configuration is unavailable; refusing to resize")
	}
	if options.Disk != 0 && int64(options.Disk)*gib < instance.Disk {
		return errors.New("machine disk shrinking is unsupported; --disk must be at least the current configured disk size")
	}
	args := []string{"edit"}
	if options.CPUs != 0 && options.CPUs != instance.CPUs {
		args = append(args, "--cpus", strconv.Itoa(options.CPUs))
	}
	if options.Memory != 0 && int64(options.Memory)*gib != instance.Memory {
		args = append(args, "--memory", strconv.Itoa(options.Memory))
	}
	if options.Disk != 0 && int64(options.Disk)*gib != instance.Disk {
		args = append(args, "--disk", strconv.Itoa(options.Disk))
	}
	if len(args) == 1 {
		_, err = fmt.Fprintln(stdout, "Resources already configured; no changes made.")
		return err
	}
	if _, err := m.command(ctx, append(args, Name)...); err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, "Resources configured. Apply them and grow the disk on the next start: casklet machine start")
	return err
}

func writeStatus(stdout io.Writer, instance Instance) (int, error) {
	return fmt.Fprintf(stdout, "%s\t%s\t%s\tcpus=%d\tmemory=%sGiB\tdisk=%sGiB (configured)\n", instance.Name, instance.Status, instance.Arch, instance.CPUs, strconv.FormatFloat(float64(instance.Memory)/float64(gib), 'f', -1, 64), strconv.FormatFloat(float64(instance.Disk)/float64(gib), 'f', -1, 64))
}
