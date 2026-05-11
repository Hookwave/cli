// Hookwave CLI entrypoint. Wires the root command tree and runs it.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/hookwave/hookwave/apps/cli/internal/cli"
)

// Build-time injected via -ldflags. Defaults are placeholders for `go run`.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := cli.Run(ctx, cli.BuildInfo{Version: version, Commit: commit, Date: date}); err != nil {
		// Cobra prints command errors itself; surface anything else to stderr.
		if !errors.Is(err, cli.ErrSilent) {
			fmt.Fprintf(os.Stderr, "hookwave: %v\n", err)
		}
		os.Exit(1)
	}
}
