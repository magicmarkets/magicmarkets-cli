// Command magicmarkets is a command-line interface for the Magic Markets v2 API.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"magicmarkets-cli/internal/cli"
)

// version is set at build time via -ldflags "-X main.version=..."
var version = "dev"

func main() {
	// Ctrl-C cancels the command's context, letting long-running commands such
	// as `stream` and `heartbeat run` shut down cleanly.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := cli.ExecuteContext(ctx, version); err != nil {
		// A cancelled context means the user interrupted us; that is not a
		// failure worth printing.
		if errors.Is(err, context.Canceled) {
			os.Exit(130)
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
