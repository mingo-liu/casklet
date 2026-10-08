package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mingo-liu/casklet/internal/image"
)

func executeManagement(r Request, stdout, stderr io.Writer) int {
	return manageOperation(r, stderr, func(ctx context.Context) (int, error) {
		if r.Action == "image-pull" {
			progress := newPullProgress(stderr, r.Progress)
			ctx = image.WithProgress(ctx, progress.observe)
		}
		return executeOperation(ctx, r, stdout)
	})
}

func manageOperation(r Request, stderr io.Writer, operation func(context.Context) (int, error)) int {
	// Keep the signal itself as well as cancellation so shell exit codes retain
	// the distinction between an interrupt and a termination request.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	defer signal.Stop(signals)
	ctx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	defer stopSignals()
	operationCtx := ctx
	if r.Action == "run" {
		var cancel context.CancelFunc
		operationCtx, cancel = context.WithTimeout(ctx, 100*time.Second)
		defer cancel()
	}
	returnCode, err := operation(operationCtx)
	if ctx.Err() != nil {
		// This context is canceled only by a signal, which is also delivered
		// to the buffered channel. Receiving it avoids a notification race.
		return 128 + int((<-signals).(syscall.Signal))
	}
	if err != nil {
		fmt.Fprintf(stderr, "casklet: %v\n", operationError(r, err))
		return 125
	}
	return returnCode
}
