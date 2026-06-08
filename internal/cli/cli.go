// Package cli wires the Cobra command tree and is the only thing
// main.go calls. Each subcommand lives in its own file in this
// directory.
package cli

import (
	"context"
	"errors"
	"os"

	"github.com/spf13/cobra"

	"github.com/hookwave/cli/internal/auth"
	"github.com/hookwave/cli/internal/config"
	"github.com/hookwave/cli/internal/httpc"
	"github.com/hookwave/cli/internal/output"
)

// BuildInfo carries values stamped at compile time via -ldflags.
type BuildInfo struct {
	Version string
	Commit  string
	Date    string
}

// ErrSilent signals that the command already printed its own error
// message; main.go should exit non-zero without re-printing.
var ErrSilent = errors.New("silent error")

// app holds the wiring shared across subcommands. Subcommands receive
// it via a context value so we don't pass it as a global.
type app struct {
	build  BuildInfo
	stdout *output.Printer
	stderr *output.Printer
	cfg    *config.Config
	// overrideToken is set when the user passes --api-key on the
	// command line (or HOOKWAVE_TOKEN env). Lets CI scripts
	// authenticate without going through device flow.
	overrideToken string
}

// authedClient builds an authenticated httpc.Client. Resolves the
// token in this order:
//   1. --api-key flag
//   2. $HOOKWAVE_TOKEN env (CI / scripts)
//   3. OS keyring (set by `hookwave login`)
// Returns a clear error when none of the above is present.
func (a *app) authedClient() (*httpc.Client, error) {
	tok := a.overrideToken
	if tok == "" {
		tok = os.Getenv("HOOKWAVE_TOKEN")
	}
	if tok == "" {
		stored, err := auth.GetToken()
		if err != nil {
			if errors.Is(err, auth.ErrNoToken) {
				return nil, errors.New("not signed in — run `hookwave login` (or set HOOKWAVE_TOKEN / pass --api-key)")
			}
			return nil, err
		}
		tok = stored
	}
	return httpc.New(a.cfg.ResolvedAPIBase(), tok, "hookwave-cli/"+a.build.Version), nil
}

// publicClient builds an unauthenticated client (for /cli/auth/device).
func (a *app) publicClient() *httpc.Client {
	return httpc.New(a.cfg.ResolvedAPIBase(), "", "hookwave-cli/"+a.build.Version)
}

type ctxKey struct{}

func withApp(ctx context.Context, a *app) context.Context {
	return context.WithValue(ctx, ctxKey{}, a)
}

func appFrom(cmd *cobra.Command) *app {
	if v, ok := cmd.Context().Value(ctxKey{}).(*app); ok {
		return v
	}
	// Should never happen — we always wire the context in Run.
	panic("hookwave cli: app not found in context")
}

// Run builds the root command and executes it.
func Run(ctx context.Context, build BuildInfo) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	a := &app{
		build:  build,
		stdout: output.NewStdout(),
		stderr: output.NewStderr(),
		cfg:    cfg,
	}

	root := &cobra.Command{
		Use:           "hookwave",
		Short:         "Hookwave CLI — webhook gateway dev tools",
		Long:          longDescription,
		SilenceErrors: false,
		SilenceUsage:  true,
		Version:       build.Version,
	}
	root.SetVersionTemplate("hookwave " + build.Version + " (" + build.Commit + ", " + build.Date + ")\n")

	// Global --api-key flag. Bound to a, applies to every subcommand
	// that calls authedClient(). Tokens are sensitive — instruct
	// users to prefer $HOOKWAVE_TOKEN to avoid putting secrets in
	// shell history.
	root.PersistentFlags().StringVar(
		&a.overrideToken,
		"api-key",
		"",
		"override stored credentials with this token (prefer $HOOKWAVE_TOKEN to avoid shell history)",
	)

	root.AddCommand(
		newLoginCmd(),
		newLogoutCmd(),
		newWhoamiCmd(),
		newOrgsCmd(),
		newInitCmd(),
		newEventsCmd(),
		newListenCmd(),
		newDoctorCmd(),
		newSourcesCmd(),
		newDestinationsCmd(),
		newConnectionsCmd(),
		newTemplatesCmd(),
		newMCPCmd(),
		newTokensCmd(),
		newConfigCmd(),
	)

	root.SetContext(withApp(ctx, a))
	return root.Execute()
}

const longDescription = `Hookwave CLI lets you receive production webhooks on your laptop, replay
past events without re-triggering the source, and operate Hookwave from
your terminal.

Get started:

  hookwave login                    # authenticate
  hookwave events list              # see recent events
  hookwave listen 3000              # forward webhooks to localhost:3000
`
