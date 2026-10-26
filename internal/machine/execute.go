package machine

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mingo-liu/casklet/internal/config"
	"github.com/mingo-liu/casklet/internal/runargs"
)

type Invocation struct {
	PreflightPorts []config.PortMapping
	RequireRunning bool
	Args           []string
	TTY            bool
	Interactive    bool
	Rootless       bool
	ImageReference string
	RefreshImage   bool
	Progress       string
}

// guestArguments maps host resources before the guest parser runs. Workload
// arguments after -- remain byte-for-byte unchanged.
func guestArguments(instance Instance, args []string) ([]string, error) {
	result := append([]string(nil), args...)
	if len(result) >= 3 && result[0] == "image" && result[1] == "import" {
		path, err := instance.hostPath(result[len(result)-1])
		if err != nil {
			return nil, err
		}
		result[len(result)-1] = path
		return result, nil
	}
	if len(result) == 0 || (result[0] != "run" && result[0] != "doctor" && result[0] != "__project-preflight") {
		return result, nil
	}
	err := runargs.Walk(result, func(name, value string, index, end int) error {
		switch name {
		case "rootfs":
			path, err := instance.hostPath(value)
			if err != nil {
				return err
			}
			value = path
		case "mount":
			mount, err := config.ParseMount(value)
			if err != nil {
				return err
			}
			if mount.Type == "volume" {
				return nil
			}
			mount.Source, err = instance.hostPath(mount.Source)
			if err != nil {
				return err
			}
			value = "type=bind,source=" + mount.Source + ",target=" + mount.Target
			if mount.ReadOnly {
				value += ",readonly"
			}
		case "p", "publish":
			mapping, err := config.ParsePortMapping(value)
			if err != nil {
				return err
			}
			if mapping.HostPort == 22 {
				return errors.New("host port 22 is reserved by Lima; publish another port")
			}
			switch mapping.HostIP {
			case "0.0.0.0":
				mapping.HostIP = "127.0.0.2"
			case "127.0.0.1":
				mapping.HostIP = "127.0.0.3"
			default:
				return errors.New("macOS published ports support host addresses 0.0.0.0 and 127.0.0.1")
			}
			value = fmt.Sprintf("%s:%d:%d/%s", mapping.HostIP, mapping.HostPort, mapping.ContainerPort, mapping.Protocol)
		default:
			return nil
		}
		runargs.SetValue(result, index, end, value)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// checkPorts catches occupied macOS ports before starting a workload. Lima owns
// the actual forwarding sockets, so this is a preflight check, not a reservation.
func checkPorts(args []string) error {
	if len(args) == 0 || args[0] != "run" {
		return nil
	}
	return visitHostResources(args, func(name, value string) error {
		if name != "p" && name != "publish" {
			return nil
		}
		mapping, err := config.ParsePortMapping(value)
		if err != nil {
			return err
		}
		if err := validateMacPort(mapping); err != nil {
			return err
		}
		if err := probePort(mapping); err != nil {
			return fmt.Errorf("macOS host port unavailable: %w; choose another host port with -p HOST_PORT:CONTAINER_PORT, or stop the process using this port", err)
		}
		return nil
	})
}

func engineCommand(rootless bool, args ...string) []string {
	command := []string{guestEngine}
	if !rootless {
		command = []string{"/usr/bin/sudo", "-n", "--", guestEngine}
	}
	return append(command, args...)
}

func Execute(ctx context.Context, invocation Invocation, stdin, stdout, stderr *os.File) (int, error) {
	if err := ctx.Err(); err != nil {
		return 125, err
	}
	// One signal source covers preparation and both credential/workload sessions.
	// Keep caller cancellation distinct from signals so guest traps retain their
	// original signal and command exit status.
	preparationCtx, cancelPreparation := context.WithCancel(ctx)
	defer cancelPreparation()
	incoming := make(chan os.Signal, 8)
	signals := make(chan os.Signal, 8)
	signal.Notify(incoming, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT, syscall.SIGWINCH)
	defer signal.Stop(incoming)
	finished, joined := make(chan struct{}), make(chan struct{})
	defer func() { close(finished); <-joined }()
	go func() {
		defer close(joined)
		for {
			select {
			case sig := <-incoming:
				select {
				case signals <- sig:
					if sig != syscall.SIGWINCH {
						cancelPreparation()
					}
				case <-finished:
					return
				}
			case <-finished:
				return
			}
		}
	}()
	if invocation.TTY && invocation.Interactive {
		if err := checkTerminal(stdin); err != nil {
			return 125, err
		}
	}
	if len(invocation.PreflightPorts) > 0 {
		if err := waitHostPorts(preparationCtx, invocation.PreflightPorts, 3*time.Second); err != nil {
			return 125, err
		}
	}
	if len(invocation.Args) > 0 && invocation.Args[0] == "__project-preflight" {
		var ports []config.PortMapping
		if err := visitHostResources(invocation.Args, func(name, value string) error {
			if name != "p" && name != "publish" {
				return nil
			}
			port, err := config.ParsePortMapping(value)
			if err == nil {
				ports = append(ports, port)
			}
			return err
		}); err != nil {
			return 125, err
		}
		if err := waitHostPorts(preparationCtx, ports, 3*time.Second); err != nil {
			return 125, err
		}
	}
	if err := localPreflight(invocation.Args); err != nil {
		return 125, err
	}
	m, err := open(stderr)
	if err != nil {
		return 125, err
	}
	if err := m.preflightShares(preparationCtx, invocation.Args); err != nil {
		return 125, err
	}
	instance, err := m.ensure(preparationCtx, defaultOptions(), false, invocation.RequireRunning)
	if err != nil {
		return 125, err
	}
	args, err := guestArguments(*instance, invocation.Args)
	if err != nil {
		return 125, err
	}
	if err := checkPorts(invocation.Args); err != nil {
		return 125, err
	}
	if invocation.ImageReference != "" {
		mode := "resolve"
		if invocation.RefreshImage {
			mode = "pull"
		}
		input, response, err := os.Pipe()
		if err != nil {
			return 125, err
		}
		defer input.Close()
		defer response.Close()
		var resolved bytes.Buffer
		output := io.Writer(&resolved)
		if invocation.RefreshImage {
			output = stdout
		}
		auth := &credentialOutput{reference: invocation.ImageReference, response: response, output: output}
		preparation := Invocation{Args: []string{"__credential-prepare", mode, invocation.Progress, invocation.ImageReference}, Rootless: invocation.Rootless}
		code, err := m.executeGuest(ctx, preparation, *instance, preparation.Args, input, stdout, stderr, auth, signals)
		if err != nil || code != 0 || invocation.RefreshImage {
			return code, err
		}
		id := strings.TrimSpace(resolved.String())
		if !strings.HasPrefix(id, "sha256:") || len(id) != 71 {
			return 125, errors.New("invalid prepared image identity")
		}
		args = preparedImageArguments(args, id)
	}
	return m.executeGuest(ctx, invocation, *instance, args, stdin, stdout, stderr, nil, signals)
}

func (m *Machine) executeGuest(ctx context.Context, invocation Invocation, instance Instance, args []string, stdin, stdout, stderr *os.File, authentication *credentialOutput, signals <-chan os.Signal) (int, error) {
	if err := ctx.Err(); err != nil {
		return 125, err
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return 125, err
	}
	token := hex.EncodeToString(random[:])
	finishWatchdog, err := startWatchdog(instance, token, invocation.Rootless)
	if err != nil {
		return 125, err
	}
	defer finishWatchdog()
	lifecycle := len(args) > 0 && (args[0] == "start" || args[0] == "restart")
	if lifecycle {
		args = append([]string{"__host-lifecycle"}, args...)
	}
	command := engineCommand(invocation.Rootless, append([]string{"__remote", token}, args...)...)
	// SSH owns terminal raw mode and resizing. Explicit control messages also
	// deliver signals for non-PTY runs, where SSH cannot forward them itself.
	archiveOutput := len(args) >= 2 && ((args[0] == "image" && args[1] == "save") || (args[0] == "volume" && args[1] == "export"))
	cmd := sshSessionCommand(context.Background(), instance, invocation.TTY, archiveOutput, command...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	checksCtx, cancelChecks := context.WithCancel(ctx)
	defer cancelChecks()
	if authentication != nil {
		cmd.Stdout = authentication
	}
	if lifecycle {
		input, response, err := os.Pipe()
		if err != nil {
			return 125, err
		}
		defer input.Close()
		defer response.Close()
		cmd.Stdin = input
		cmd.Stdout = &portCheckOutput{ctx: checksCtx, response: response, stdout: stdout}
	}
	var inspection *inspectionOutput
	if len(invocation.Args) > 0 && invocation.Args[0] == "inspect" {
		inspection = &inspectionOutput{}
		cmd.Stdout = inspection
	}
	if err := ctx.Err(); err != nil {
		return 125, err
	}
	if err := cmd.Start(); err != nil {
		return 125, err
	}
	if authentication != nil {
		// SSH inherited its own descriptor. Keeping this read end in the host
		// would prevent EPIPE if SSH exits before consuming the credentials.
		_ = stdin.Close()
	}
	grace := 75 * time.Second
	if archiveOutput {
		// Bound SSH draining when a host archive pipe is full.
		grace = 2 * time.Second
	}
	interrupted, err := waitGuest(ctx, cmd, signals, grace, func(sig syscall.Signal) {
		cancelChecks()
		controlCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := m.sendSignal(controlCtx, instance, invocation.Rootless, token, strconv.Itoa(int(sig))); err != nil {
			fmt.Fprintf(stderr, "casklet: signal forwarding: %v\n", err)
		}
	})
	if archiveOutput && interrupted != 0 {
		return 128 + int(interrupted), nil
	}
	if ctx.Err() != nil {
		return 125, ctx.Err()
	}
	if authentication != nil && err == nil && !authentication.completed && authentication.err == nil {
		authentication.err = errors.New("incomplete registry preparation response")
	}
	if authentication != nil && authentication.err != nil {
		return 125, authentication.err
	}
	return guestResult(instance, inspection, stdout, err)
}
