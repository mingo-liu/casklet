package machine

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// waitGuest lets the remote command clean up before bounding the SSH lifetime.
// The started command is always reaped, including when forwarding fails.
func waitGuest(ctx context.Context, cmd *exec.Cmd, signals <-chan os.Signal, grace time.Duration, forward func(syscall.Signal)) (syscall.Signal, error) {
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	cancellation := ctx.Done()
	var (
		canceled    error
		interrupted syscall.Signal
		timer       *time.Timer
		shutdown    <-chan time.Time
	)
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	stop := func(sig syscall.Signal) {
		if timer == nil {
			timer = time.NewTimer(grace)
			shutdown = timer.C
		}
		forward(sig)
	}
	for {
		select {
		case err := <-done:
			if canceled != nil {
				return 0, canceled
			}
			return interrupted, err
		case sig, ok := <-signals:
			if !ok {
				signals = nil
				continue
			}
			if sig == syscall.SIGWINCH {
				_ = cmd.Process.Signal(sig)
				continue
			}
			number, ok := sig.(syscall.Signal)
			if !ok {
				continue
			}
			if interrupted == 0 {
				interrupted = number
			}
			stop(number)
		case <-cancellation:
			cancellation = nil
			canceled = ctx.Err()
			if timer == nil {
				stop(syscall.SIGTERM)
			}
		case <-shutdown:
			_ = cmd.Process.Kill()
			//lint:ignore S1005 The shutdown error below takes precedence over the reaped result.
			_ = <-done
			if canceled != nil {
				return 0, canceled
			}
			return interrupted, errors.New("guest did not finish after signal forwarding")
		}
	}
}
