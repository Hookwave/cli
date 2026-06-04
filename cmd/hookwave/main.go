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
	"github.com/hookwave/hookwave/apps/cli/internal/httpc"
)

// Build-time injected via -ldflags. Defaults are placeholders for `go run`.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// Exit codes — distinguishable so CI scripts can branch:
//   0  success
//   1  generic / unexpected error
//   3  bad request (4xx other than auth/not-found)
//   4  unauthorized / forbidden
//   5  not found
//   6  conflict (409)
//   7  rate limited (429)
//   8  server error (5xx)
const (
	exitOK         = 0
	exitGeneric    = 1
	exitBadRequest = 3
	exitAuth       = 4
	exitNotFound   = 5
	exitConflict   = 6
	exitRateLimit  = 7
	exitServer     = 8
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := cli.Run(ctx, cli.BuildInfo{Version: version, Commit: commit, Date: date}); err != nil {
		// Cobra prints command errors itself; surface anything else to stderr.
		if !errors.Is(err, cli.ErrSilent) {
			fmt.Fprintf(os.Stderr, "hookwave: %v\n", err)
		}
		os.Exit(exitCodeFor(err))
	}
}

func exitCodeFor(err error) int {
	var apiErr *httpc.APIError
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.Status == 401 || apiErr.Status == 403:
			return exitAuth
		case apiErr.Status == 404:
			return exitNotFound
		case apiErr.Status == 409:
			return exitConflict
		case apiErr.Status == 429:
			return exitRateLimit
		case apiErr.Status >= 400 && apiErr.Status < 500:
			return exitBadRequest
		case apiErr.Status >= 500:
			return exitServer
		}
	}
	return exitGeneric
}
