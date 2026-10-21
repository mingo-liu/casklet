//go:build darwin

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/mingo-liu/casklet/internal/machine"
	"github.com/mingo-liu/casklet/internal/project"
	"github.com/mingo-liu/casklet/internal/registryauth"
)

func platformProjectReservations(data []byte) ([]byte, error) {
	var page project.ReservationPage
	if err := json.Unmarshal(data, &page); err != nil {
		return nil, fmt.Errorf("invalid project reservation response")
	}
	for i := range page.Records {
		for j := range page.Records[i].Publish {
			port := &page.Records[i].Publish[j]
			port.HostIP = machine.HostPortAddress(port.HostIP)
		}
	}
	return json.Marshal(page)
}

func platformArguments(args []string) []string {
	if len(args) == 0 || (args[0] != "run" && args[0] != "doctor" && args[0] != "__project-preflight") {
		return args
	}
	booleans := map[string]bool{"d": true, "detach": true, "i": true, "interactive": true, "t": true, "tty": true, "it": true, "ti": true, "read-only": true, "rootless": true, "userns": true, "no-healthcheck": true, "json": true}
	for i := 1; i < len(args); i++ {
		arg := args[i]
		if len(arg) < 2 || arg[0] != '-' || arg == "--" {
			break
		}
		name := arg[1:]
		if name[0] == '-' {
			name = name[1:]
		}
		if name == "" || name[0] == '-' {
			break
		}
		name, _, inline := strings.Cut(name, "=")
		if name == "rootfs" || name == "image" {
			return args
		}
		if !inline && !booleans[name] {
			i++
		}
	}
	return append([]string{args[0], "--rootfs", machine.BuiltinRootFS}, args[1:]...)
}

func platformUsage(topic string) (string, error) {
	if hostHelpTopic(topic) {
		return machine.Help(topic)
	}
	return scopedUsage(topic, true)
}

func executeHostCommand(args []string, stdin, stdout, stderr *os.File) (bool, int) {
	if len(args) == 0 || (args[0] != "machine" && args[0] != "rootfs") {
		return false, 0
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := machine.HostCommand(ctx, args, stdout, stderr); err != nil {
		fmt.Fprintf(stderr, "casklet: %v\n", err)
		return true, 125
	}
	return true, 0
}

func executePlatform(args []string, request Request, stdin, stdout, stderr *os.File) (bool, int) {
	if request.Action == "login" || request.Action == "logout" {
		store, err := registryauth.OpenStore()
		if err == nil {
			err = executeRegistryCommand(request, stdin, stdout, store)
		}
		if err != nil {
			fmt.Fprintf(stderr, "casklet: %v\n", err)
			return true, 125
		}
		return true, 0
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if request.Action == "doctor" {
		return true, executeDarwinDoctor(ctx, args, request, stdin, stdout, stderr)
	}
	paths, err := hostPathArguments(args, request.Action)
	if err != nil {
		fmt.Fprintf(stderr, "casklet: %v\n", err)
		return true, 125
	}
	paths = guestProgressArguments(paths, request, stderr)
	invocation := machine.Invocation{RequireRunning: os.Getenv(projectPreviewEnvironment) == "1", Args: paths, TTY: request.Config.TTY || request.Exec.TTY, Interactive: request.Config.Interactive || request.Exec.Interactive, Rootless: request.Config.Rootless}
	if request.Action == "project-start-preflight" {
		invocation.PreflightPorts = request.Config.Publish
	}
	if request.Action == "image-pull" || (request.Action == "run" || request.Action == "project-preflight") && request.Config.Image != "" {
		invocation.ImageReference, invocation.RefreshImage, invocation.Progress = request.Config.Image, request.Action == "image-pull", progressMode(request.Progress, stderr)
		if invocation.RefreshImage {
			invocation.ImageReference = request.Reference
		}
	}
	code, err := machine.Execute(ctx, invocation, stdin, stdout, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "casklet: %v\n", err)
	}
	return true, code
}
