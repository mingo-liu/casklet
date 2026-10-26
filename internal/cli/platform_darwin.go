//go:build darwin

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/mingo-liu/casklet/internal/machine"
	"github.com/mingo-liu/casklet/internal/project"
	"github.com/mingo-liu/casklet/internal/registryauth"
	"github.com/mingo-liu/casklet/internal/runargs"
)

func hostPublishedPorts(records []containerListing) {
	for i := range records {
		for j := range records[i].Publish {
			port := &records[i].Publish[j]
			port.HostIP = machine.HostPortAddress(port.HostIP)
		}
	}
}

func platformProjectRecords(data []byte) ([]byte, error) {
	var page projectRecordPage
	if err := json.Unmarshal(data, &page); err != nil {
		return nil, fmt.Errorf("invalid project record response")
	}
	hostPublishedPorts(page.Records)
	return json.Marshal(page)
}

func executeDarwinPS(ctx context.Context, args []string, r Request, stdin, stdout, stderr *os.File) (int, error) {
	// Render on the host so Lima's forwarding addresses never appear as Mac ports.
	file, err := os.CreateTemp("", "casklet-ps-*")
	if err != nil {
		return 125, err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	args = append(append([]string(nil), args...), "--json=true")
	code, err := machine.Execute(ctx, machine.Invocation{Args: args, RequireRunning: os.Getenv(projectPreviewEnvironment) == "1"}, stdin, file, stderr)
	if err != nil || code != 0 {
		return code, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return 125, err
	}
	var records []containerListing
	if err := json.NewDecoder(file).Decode(&records); err != nil {
		return 125, fmt.Errorf("invalid guest container listing: %w", err)
	}
	hostPublishedPorts(records)
	if err := writeListings(stdout, records, r.JSON); err != nil {
		return 125, err
	}
	return 0, nil
}

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
	hasSource := false
	_ = runargs.Walk(args, func(name, _ string, _, _ int) error {
		hasSource = hasSource || name == "rootfs" || name == "image"
		return nil
	})
	if hasSource {
		return args
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
	// Machine execution owns signals across preparation and remote sessions.
	ctx := context.Background()
	if request.Action == "doctor" {
		return true, executeDarwinDoctor(ctx, args, request, stdin, stdout, stderr)
	}
	if request.Action == "ps" {
		code, err := executeDarwinPS(ctx, args, request, stdin, stdout, stderr)
		if err != nil {
			fmt.Fprintf(stderr, "casklet: %v\n", err)
		}
		return true, code
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
