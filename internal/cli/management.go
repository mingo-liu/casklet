package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func executeManagement(r Request, stdout, stderr io.Writer) int {
	// Keep the signal itself as well as cancellation so shell exit codes retain
	// the distinction between an interrupt and a termination request.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	ctx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	operationCtx := ctx
	if r.Action == "run" {
		var cancel context.CancelFunc
		operationCtx, cancel = context.WithTimeout(ctx, 100*time.Second)
		defer cancel()
	}
	returnCode, err := executeOperation(operationCtx, r, stdout)
	if ctx.Err() != nil {
		// This context is canceled only by a signal, which is also delivered
		// to the buffered channel. Receiving it avoids a notification race.
		if <-signals == syscall.SIGTERM {
			return 143
		}
		return 130
	}
	if err != nil {
		fmt.Fprintf(stderr, "mdocker: %v\n", operationError(r, err))
		return 125
	}
	return returnCode
}
