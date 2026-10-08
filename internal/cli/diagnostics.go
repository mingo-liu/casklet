package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/mingo-liu/mini-docker/internal/container"
	"github.com/mingo-liu/mini-docker/internal/image"
)

func argumentError(args []string, err error) error {
	topic := ""
	if len(args) > 0 {
		switch args[0] {
		case "run", "exec", "doctor", "ps", "inspect", "stats", "wait", "start", "restart", "stop", "logs", "rm":
			topic = args[0]
		case "image":
			topic = "image"
			if len(args) > 1 && (args[1] == "import" || args[1] == "pull" || args[1] == "ls" || args[1] == "rm") {
				topic += " " + args[1]
			}
		}
	}
	command := "mdocker help"
	if topic != "" {
		command = "mdocker " + topic + " --help"
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
		hint = "list available containers with mdocker ps -a; use their full ID or exact name."
	case errors.Is(err, container.ErrNotTerminal):
		hint = "stop the container with mdocker stop " + ref + ", then retry mdocker rm " + ref + "."
	case errors.Is(err, container.ErrNameInUse):
		hint = "choose another --name, or inspect the existing container with mdocker inspect " + quoteCLIArgument(r.Name) + "."
	case errors.Is(err, container.ErrBusy):
		hint = "another lifecycle operation is still active; inspect the container with mdocker inspect " + ref + " and retry after it finishes."
	case errors.Is(err, image.ErrNotFound):
		hint = "list cached images with mdocker image ls; use a full sha256: ID or pull a registry reference with mdocker image pull NAME."
	case errors.Is(err, image.ErrInUse):
		hint = "find referencing containers with mdocker ps -a and mdocker inspect NAME; remove those containers before retrying image deletion."
	}
	if hint == "" {
		return err
	}
	return fmt.Errorf("%w\nHint: %s", err, hint)
}
