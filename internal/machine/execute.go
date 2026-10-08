package machine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mingo-liu/mini-docker/internal/config"
)

type Invocation struct {
	Args        []string
	TTY         bool
	Interactive bool
	Rootless    bool
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
	if len(result) == 0 || (result[0] != "run" && result[0] != "doctor") {
		return result, nil
	}
	booleans := map[string]bool{"d": true, "detach": true, "i": true, "interactive": true, "t": true, "tty": true, "it": true, "ti": true, "read-only": true, "rootless": true, "userns": true}
	for i := 1; i < len(result); i++ {
		arg := result[i]
		if arg == "--" {
			break
		}
		name, value, inline := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		if booleans[name] {
			continue
		}
		index := i
		if !inline {
			i++
			index = i
			if i >= len(result) {
				return nil, fmt.Errorf("missing value for %s", arg)
			}
			value = result[i]
		}
		switch name {
		case "rootfs":
			path, err := instance.hostPath(value)
			if err != nil {
				return nil, err
			}
			value = path
		case "mount":
			mount, err := config.ParseMount(value)
			if err != nil {
				return nil, err
			}
			mount.Source, err = instance.hostPath(mount.Source)
			if err != nil {
				return nil, err
			}
			value = "type=bind,source=" + mount.Source + ",target=" + mount.Target
			if mount.ReadOnly {
				value += ",readonly"
			}
		case "p", "publish":
			mapping, err := config.ParsePortMapping(value)
			if err != nil {
				return nil, err
			}
			if mapping.HostPort == 22 {
				return nil, errors.New("host port 22 is reserved by Lima; publish another port")
			}
			switch mapping.HostIP {
			case "0.0.0.0":
				mapping.HostIP = "127.0.0.2"
			case "127.0.0.1":
				mapping.HostIP = "127.0.0.3"
			default:
				return nil, errors.New("macOS published ports support host addresses 0.0.0.0 and 127.0.0.1")
			}
			value = fmt.Sprintf("%s:%d:%d/%s", mapping.HostIP, mapping.HostPort, mapping.ContainerPort, mapping.Protocol)
		default:
			continue
		}
		if inline {
			result[index] = strings.SplitN(arg, "=", 2)[0] + "=" + value
		} else {
			result[index] = value
		}
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
	if invocation.TTY && invocation.Interactive {
		if err := checkTerminal(stdin); err != nil {
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
	if err := m.preflightShares(ctx, invocation.Args); err != nil {
		return 125, err
	}
	instance, err := m.ensure(ctx, defaultOptions(), false)
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
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return 125, err
	}
	token := hex.EncodeToString(random[:])
	finishWatchdog, err := startWatchdog(*instance, token, invocation.Rootless)
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
	cmd := sshCommand(context.Background(), *instance, invocation.TTY, command...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	if lifecycle {
		input, response, err := os.Pipe()
		if err != nil {
			return 125, err
		}
		defer input.Close()
		defer response.Close()
		cmd.Stdin = input
		cmd.Stdout = &portCheckOutput{ctx: ctx, response: response, stdout: stdout}
	}
	var inspection *inspectionOutput
	if len(invocation.Args) > 0 && invocation.Args[0] == "inspect" {
		inspection = &inspectionOutput{}
		cmd.Stdout = inspection
	}
	signals := make(chan os.Signal, 8)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT, syscall.SIGWINCH)
	defer signal.Stop(signals)
	if err := cmd.Start(); err != nil {
		return 125, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var shutdown <-chan time.Time
	for {
		select {
		case err := <-done:
			return guestResult(*instance, inspection, stdout, err)
		case signal := <-signals:
			if signal == syscall.SIGWINCH {
				_ = cmd.Process.Signal(signal)
				continue
			}
			controlCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, controlErr := m.output(controlCtx, *instance, engineCommand(invocation.Rootless, "__signal", token, strconv.Itoa(int(signal.(syscall.Signal))))...)
			cancel()
			if controlErr != nil {
				fmt.Fprintf(stderr, "mdocker: signal forwarding: %v\n", controlErr)
			}
			if shutdown == nil {
				shutdown = time.After(75 * time.Second)
			}
		case <-shutdown:
			_ = cmd.Process.Kill()
			<-done
			return 125, errors.New("guest did not finish after signal forwarding")
		}
	}
}
