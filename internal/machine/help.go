package machine

import (
	"errors"
	"flag"
	"fmt"
	"strings"
)

var hostHelp = map[string]string{
	"machine": `Usage: mdocker machine COMMAND

Commands:
  init    Create the product VM with initial resources and shares
  start   Create or start the VM and install the bundled engine
  stop    Stop the VM, terminating workloads and preserving files
  status  Show VM status without creating or starting it
  share   Add a writable share while the VM is stopped

Notes:
  Run as your regular Mac user. Requires macOS 13.5+ and Lima 2.0+.
  Install Lima with brew install lima. The first container command creates the VM.
  Run mdocker machine COMMAND --help for command syntax and examples.

Examples:
  mdocker machine status
  mdocker machine init --cpus 4 --memory 4 --disk 20
`,
	"machine init": `Usage: mdocker machine init [--cpus N] [--memory GiB] [--disk GiB] [--mount DIRECTORY ...]

Options:
  --cpus    CPU count, 1-64 (default: 4)
  --memory  Memory in GiB, 1-128 (default: 4)
  --disk    Disk in GiB, 4-1024 (default: 20)
  --mount   Additional existing Mac directory shared writable; repeat to add shares

Notes:
  Creates the product VM; options apply only before it exists.
  The home directory is already shared. Shares cannot overlap or cover /.
  To add a share to an existing VM, stop it and use mdocker machine share DIRECTORY.
  Run as your regular Mac user; requires macOS 13.5+ and Lima 2.0+.

Examples:
  mdocker machine init --cpus 4 --memory 4 --disk 20
  mdocker machine init --mount /Volumes/ContainerData
`,
	"machine start": `Usage: mdocker machine start

Options:
  -h, --help  Show this help

Notes:
  Creates or starts the product VM and installs the current bundled engine.
  Repeatable; keeps container and image storage. Containers stopped with the VM
  stay stopped until mdocker start or mdocker restart is used.

Examples:
  mdocker machine start
  mdocker start worker
`,
	"machine stop": `Usage: mdocker machine stop

Options:
  -h, --help  Show this help

Notes:
  Terminates running containers and stops the product VM, preserving their files.
  Repeatable; a missing or already stopped VM does not need to be created.

Examples:
  mdocker machine stop
  mdocker machine status
`,
	"machine status": `Usage: mdocker machine status

Options:
  -h, --help  Show this help

Notes:
  Shows the product VM's name, state, and architecture.
  Does not create or start a VM. A missing VM appears as not initialized.

Examples:
  mdocker machine status
  mdocker machine start
`,
	"machine share": `Usage: mdocker machine share DIRECTORY

Options:
  -h, --help  Show this help

Notes:
  Adds an existing Mac directory as a writable share without replacing VM storage.
  The VM must exist and be stopped; stopping terminates workloads and preserves files.
  The home is already shared. Shares cannot overlap or cover /.
  An already shared writable directory is accepted without changes.

Examples:
  mdocker machine stop
  mdocker machine share /Volumes/ContainerData
  mdocker machine start
`,
	"rootfs": `Usage: mdocker rootfs DIRECTORY

Options:
  -h, --help  Show this help

Notes:
  Exports the built-in static BusyBox template to a new shared Mac directory.
  The destination must not exist; missing parents are created after validation.
  Creates or starts the product VM if needed. Default runs already use builtin:busybox.

Examples:
  mdocker rootfs ./rootfs/busybox
  mdocker run --rootfs ./rootfs/busybox -- /bin/echo hello
`,
}

// Help returns scoped host-command help without opening Lima or local state.
func Help(topic string) (string, error) {
	if text, exists := hostHelp[topic]; exists {
		return text, nil
	}
	return "", fmt.Errorf("unknown help topic %q\nHint: run mdocker machine --help for available host commands.", topic)
}

func isHelpOption(value string) bool { return value == "--help" || value == "-h" }

// directHostHelp checks only option positions, never a directory or option value.
func directHostHelp(args []string) (bool, error) {
	if len(args) < 2 || (args[0] != "machine" && args[0] != "rootfs") {
		return false, nil
	}
	position, topic := 1, args[0]
	if args[0] == "machine" && !isHelpOption(args[1]) && len(args) > 2 {
		position, topic = 2, "machine "+args[1]
	}
	if !isHelpOption(args[position]) {
		return false, nil
	}
	if _, err := Help(topic); err != nil {
		return true, err
	}
	if len(args) != position+1 {
		return true, fmt.Errorf("%s help accepts no additional arguments; use mdocker help %s", topic, topic)
	}
	return true, flag.ErrHelp
}

func initHelpResult(options []string) error {
	for i := 0; i < len(options); i++ {
		if isHelpOption(options[i]) {
			if i != len(options)-1 {
				return errors.New("machine init help accepts no additional arguments; use mdocker help machine init")
			}
			return flag.ErrHelp
		}
		_, _, inline := strings.Cut(options[i], "=")
		if !inline {
			i++ // All machine init options take exactly one value.
		}
	}
	return flag.ErrHelp
}

func hostCommandTopic(args []string) string {
	if len(args) > 0 && args[0] == "rootfs" {
		return "rootfs"
	}
	if len(args) > 1 && args[0] == "machine" {
		if _, exists := hostHelp["machine "+args[1]]; exists {
			return "machine " + args[1]
		}
	}
	return "machine"
}
