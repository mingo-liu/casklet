package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/mingo-liu/casklet/internal/container"
	"github.com/mingo-liu/casklet/internal/image"
	"github.com/mingo-liu/casklet/internal/volume"
)

func argumentError(args []string, err error) error {
	topic := ""
	if len(args) > 0 {
		switch args[0] {
		case "run", "exec", "doctor", "ps", "inspect", "stats", "wait", "start", "restart", "stop", "logs", "rm":
			topic = args[0]
		case "system":
			topic = "system"
			if len(args) > 1 && args[1] == "df" {
				topic += " df"
			}
		case "volume":
			topic = "volume"
			if len(args) > 1 && (args[1] == "create" || args[1] == "ls" || args[1] == "inspect" || args[1] == "rm") {
				topic += " " + args[1]
			}
		case "image":
			topic = "image"
			if len(args) > 1 && (args[1] == "import" || args[1] == "pull" || args[1] == "ls" || args[1] == "rm" || args[1] == "prune") {
				topic += " " + args[1]
			}
		}
	}
	command := "casklet help"
	if topic != "" {
		command = "casklet " + topic + " --help"
	}
	return fmt.Errorf("%w\nHint: run %s for usage and examples.", err, command)
}

func quoteCLIArgument(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func operationError(r Request, err error) error {
	hint := ""
	ref := quoteCLIArgument(r.Reference)
	switch {
	case errors.Is(err, container.ErrNotFound):
		hint = "list available containers with casklet ps -a; use their full ID or exact name."
	case errors.Is(err, container.ErrNotTerminal):
		hint = "stop the container with casklet stop " + ref + ", then retry casklet rm " + ref + "."
	case errors.Is(err, container.ErrNameInUse):
		hint = "choose another --name, or inspect the existing container with casklet inspect " + quoteCLIArgument(r.Name) + "."
	case errors.Is(err, container.ErrBusy):
		hint = "another lifecycle operation is still active; inspect the container with casklet inspect " + ref + " and retry after it finishes."
	case errors.Is(err, image.ErrNotFound):
		hint = "list cached images with casklet image ls --json; use a full sha256: ID or pull a registry reference with casklet image pull NAME."
	case errors.Is(err, volume.ErrInUse):
		hint = "remove referencing containers before retrying volume deletion; active foreground runs must also finish."
	case errors.Is(err, image.ErrInUse):
		hint = "find referencing containers with casklet ps -a and casklet inspect NAME; remove those containers before retrying image deletion."
	}
	if hint == "" {
		return err
	}
	return fmt.Errorf("%w\nHint: %s", err, hint)
}
