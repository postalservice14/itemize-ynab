// Command itemize-ynab splits Walmart card charges in YNAB by category.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/postalservice14/itemize-ynab/internal/cli"
)

func main() {
	// Ctrl-C and SIGTERM cancel the context, so a run stops cleanly and still
	// prints its partial summary.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	// After the first signal restore the default behavior, so a second Ctrl-C
	// kills the process instead of waiting for the run to wind down.
	go func() {
		<-ctx.Done()
		stop()
	}()
	code := cli.Run(ctx, os.Args[1:], cli.OSEnv())
	stop()
	os.Exit(code)
}
