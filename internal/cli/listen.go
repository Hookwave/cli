package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/hookwave/hookwave/apps/cli/internal/output"
	"github.com/hookwave/hookwave/apps/cli/internal/tui"
	"github.com/hookwave/hookwave/apps/cli/internal/tunnel"
)

func newListenCmd() *cobra.Command {
	var (
		sources       []string
		host          string
		noTUI         bool
		timeout       time.Duration
		filterBody    string
		filterHeaders string
		filterPath    string
		filterQuery   string
	)
	cmd := &cobra.Command{
		Use:   "listen <port-or-url>",
		Short: "Forward webhooks from Hookwave to a local server",
		Long: `Forward webhooks from your Hookwave sources to a local URL.

Examples:
  hookwave listen 3000                     # → http://localhost:3000
  hookwave listen http://localhost:8000    # explicit URL
  hookwave listen 3000 --source src_abc    # only one source
  hookwave listen 3000 --no-tui            # plain log output, no TUI`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a := appFrom(cmd)
			if _, err := a.authedClient(); err != nil {
				// Wrap so the user sees a single clear line. Without this
				// they got "" on a stale CLI token because cobra was
				// printing nothing recognisable.
				return fmt.Errorf("hookwave listen: %w (try `hookwave login` to refresh credentials)", err)
			}
			localURL, err := normalizeLocalTarget(args[0], host)
			if err != nil {
				return err
			}

			// Auto-fall-back to plain log mode when stdout isn't a real
			// TTY. Bubble Tea's alt-screen renderer silently bails when
			// it can't take over the terminal — which is why the user saw
			// `listen` exit with no output before this guard existed.
			if !noTUI && !term.IsTerminal(int(os.Stdout.Fd())) {
				a.stderr.Println(output.Warn,
					"→ stdout is not a TTY; falling back to --no-tui plain log mode.")
				noTUI = true
			}

			tok, _ := readToken()
			opts := tunnel.Options{
				APIBase:       a.cfg.ResolvedAPIBase(),
				Token:         tok,
				SourceIDs:     sources,
				FilterBody:    filterBody,
				FilterHeaders: filterHeaders,
				FilterPath:    filterPath,
				FilterQuery:   filterQuery,
				LocalURL:      localURL,
				LocalTimeout:  timeout,
				UserAgent:     "hookwave-cli/" + a.build.Version,
			}

			if noTUI {
				opts.OnEvent = func(e *tunnel.Event, status int, ms int, err error) {
					if err != nil {
						a.stderr.Printf(output.Error, "✗ %s %s → error: %v\n", e.Method, e.Path, err)
						return
					}
					tone := output.Success
					if status >= 400 {
						tone = output.Error
					} else if status >= 300 {
						tone = output.Warn
					}
					a.stdout.Printf(tone, "%s %s → %d (%dms)\n", e.Method, e.Path, status, ms)
				}
				a.stdout.Printf(output.Muted, "→ Forwarding to %s. Press Ctrl-C to stop.\n", localURL)
				if err := tunnel.Run(cmd.Context(), opts); err != nil && !errors.Is(err, context.Canceled) {
					return err
				}
				return nil
			}

			authed, err := a.authedClient()
			if err != nil {
				return fmt.Errorf("hookwave listen: %w", err)
			}
			// Surface a notice on stderr before the TUI takes over the
			// screen. If the TUI fails to render for any reason the user
			// at least sees this instead of a silent return to prompt.
			a.stderr.Printf(output.Muted,
				"→ Forwarding to %s. Press q or Ctrl-C to exit the TUI.\n", localURL)
			if err := tui.Run(cmd.Context(), tui.Options{
				LocalURL:     localURL,
				TunnelOpts:   opts,
				ActiveOrg:    a.cfg.ActiveOrg,
				UserEmail:    a.cfg.UserEmail,
				CLIVersion:   a.build.Version,
				API:          authed,
				DashboardURL: dashboardURLFromAPI(a.cfg.ResolvedAPIBase()),
			}); err != nil {
				return fmt.Errorf("hookwave listen: tui exited: %w", err)
			}
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&sources, "source", nil, "limit to one or more source ids (repeatable, comma-separated)")
	cmd.Flags().StringVar(&host, "host", "127.0.0.1", "local host to forward to (when only a port is given)")
	cmd.Flags().BoolVar(&noTUI, "no-tui", false, "stream plain log lines instead of the interactive TUI")
	cmd.Flags().DurationVar(&timeout, "timeout", 30*time.Second, "per-request timeout when forwarding to local")
	cmd.Flags().StringVar(&filterBody, "filter-body", "", "only forward events whose body contains this substring")
	cmd.Flags().StringVar(&filterHeaders, "filter-headers", "", "only forward events whose headers contain this substring")
	cmd.Flags().StringVar(&filterPath, "filter-path", "", "only forward events whose path contains this substring")
	cmd.Flags().StringVar(&filterQuery, "filter-query", "", "only forward events whose query string contains this substring")
	return cmd
}

// normalizeLocalTarget accepts "3000", "localhost:3000", or a full URL
// and returns a normalized "scheme://host:port" string. Path is left
// for the tunnel to append per event.
func normalizeLocalTarget(arg, host string) (string, error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return "", errors.New("port or URL is required")
	}
	if strings.HasPrefix(arg, "http://") || strings.HasPrefix(arg, "https://") {
		return strings.TrimRight(arg, "/"), nil
	}
	// Plain port?
	if _, err := strconv.Atoi(arg); err == nil {
		if host == "" {
			host = "127.0.0.1"
		}
		return fmt.Sprintf("http://%s:%s", host, arg), nil
	}
	// host:port shorthand?
	if _, _, err := net.SplitHostPort(arg); err == nil {
		return "http://" + arg, nil
	}
	return "", fmt.Errorf("don't know how to interpret %q (expected port or URL)", arg)
}

// readToken reuses internal/auth without importing it (avoids a cycle
// when the cli package needs it). Cleaner: just import directly.
func readToken() (string, error) {
	// Imported lazily here to keep the listen command file's imports
	// uncluttered. Equivalent to a direct call.
	return tokenFromAuth()
}

// dashboardURLFromAPI infers the web app URL from the API base by
// stripping the leading "api." subdomain when present. Used for the
// `o` keybinding in the TUI. Returns an empty string when we can't
// confidently guess (custom domains, IP addresses, etc.).
func dashboardURLFromAPI(api string) string {
	api = strings.TrimRight(api, "/")
	switch {
	case strings.Contains(api, "://api."):
		return strings.Replace(api, "://api.", "://app.", 1)
	case strings.Contains(api, "localhost:3002") || strings.Contains(api, "127.0.0.1:3002"):
		return strings.Replace(strings.Replace(api, ":3002", ":5173", 1), "127.0.0.1", "localhost", 1)
	}
	return ""
}
