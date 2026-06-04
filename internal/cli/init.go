package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/hookwave/hookwave/apps/cli/internal/httpc"
	"github.com/hookwave/hookwave/apps/cli/internal/output"
)

// `hookwave init` — interactive scaffolding. Creates a source, writes
// .hookwave.toml in cwd, and emits a starter handler in the user's
// preferred language. Designed to take a developer from "I have a
// shell" to "webhooks flowing into my code" in under a minute.
//
// Two prompt paths share the same logic:
//   • TUI (default in a real terminal) — Bubble Tea + huh
//   • Bare line prompts (`--no-tui`, or non-TTY stdout) — kept so
//     CI runners and dumb terminals still work.

var supportedProviders = []string{"stripe", "shopify", "github", "replicate", "lemonsqueezy", "twilio", "generic"}
var supportedHandlers = []string{"typescript", "javascript", "python", "go", "none"}

// initInputs is the data both prompt paths produce. The actual create
// step doesn't care how the values were collected.
type initInputs struct {
	Provider    string
	Name        string
	Port        int
	Handler     string
	Force       bool
}

type sourceCreateBody struct {
	Name     string `json:"name"`
	Provider string `json:"provider"`
}

type sourceCreateResp struct {
	Data struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Provider string `json:"provider"`
	} `json:"data"`
	IngestURL string `json:"ingest_url"`
}

func newInitCmd() *cobra.Command {
	var (
		flagName     string
		flagProvider string
		flagPort     int
		flagHandler  string
		flagForce    bool
		flagNoTUI    bool
	)
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Scaffold a new source + starter handler in the current directory",
		Long: `Creates a Hookwave source for your project and drops a starter handler.
Run from the root of your repo. Writes:

  .hookwave.toml             — local config (source id, ingest URL, default port)
  handlers/<provider>.<ext>  — starter handler stub for your language

Re-running is safe: existing files are detected and left alone unless --force.

Defaults to an interactive TUI when stdout is a real terminal. Pass
--no-tui to use plain prompts (useful for CI / scripts).`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a := appFrom(cmd)
			c, err := a.authedClient()
			if err != nil {
				return err
			}

			if _, err := os.Stat(".hookwave.toml"); err == nil && !flagForce {
				return errors.New(".hookwave.toml already exists — pass --force to overwrite")
			}

			// Pre-fill any flags the user passed; the prompt path
			// only asks for the missing ones.
			seed := initInputs{
				Provider: strings.ToLower(strings.TrimSpace(flagProvider)),
				Name:     strings.TrimSpace(flagName),
				Port:     flagPort,
				Handler:  strings.ToLower(strings.TrimSpace(flagHandler)),
				Force:    flagForce,
			}

			// Validate eager flags — fail fast on bad provider rather
			// than letting it surface server-side later.
			if seed.Provider != "" && !contains(supportedProviders, seed.Provider) {
				return fmt.Errorf("provider %q not supported (one of: %s)", seed.Provider, strings.Join(supportedProviders, ", "))
			}
			if seed.Handler != "" && !contains(supportedHandlers, seed.Handler) {
				return fmt.Errorf("handler %q not supported (one of: %s)", seed.Handler, strings.Join(supportedHandlers, ", "))
			}

			// Pick a sensible default for handler when stack is detectable.
			if seed.Handler == "" {
				seed.Handler = detectStack(".")
			}

			useTUI := !flagNoTUI && term.IsTerminal(int(os.Stdout.Fd()))
			var inputs initInputs
			if useTUI {
				v, err := promptInitTUI(seed)
				if err != nil {
					return err
				}
				inputs = v
			} else {
				v, err := promptInitBare(a.stdout, seed)
				if err != nil {
					return err
				}
				inputs = v
			}

			return runInitWithInputs(cmd.Context(), a, c, inputs)
		},
	}
	cmd.Flags().StringVar(&flagName, "name", "", "source name (skips prompt)")
	cmd.Flags().StringVar(&flagProvider, "provider", "", "provider: stripe|shopify|github|replicate|lemonsqueezy|twilio|generic (skips prompt). Schedule sources are dashboard-only.")
	cmd.Flags().IntVar(&flagPort, "port", 0, "local port (skips prompt)")
	cmd.Flags().StringVar(&flagHandler, "handler", "", "starter handler language: typescript|javascript|python|go|none")
	cmd.Flags().BoolVar(&flagForce, "force", false, "overwrite existing files without prompting")
	cmd.Flags().BoolVar(&flagNoTUI, "no-tui", false, "use plain text prompts instead of the interactive form")
	return cmd
}

// promptInitBare is the original line-prompt flow. Used when not on a
// TTY or when the user explicitly passes --no-tui.
func promptInitBare(out *output.Printer, seed initInputs) (initInputs, error) {
	r := newPromptReader()
	in := seed

	if in.Provider == "" {
		p, err := r.choose(out, "Provider", supportedProviders, "generic")
		if err != nil {
			return initInputs{}, err
		}
		in.Provider = p
	}
	if in.Name == "" {
		def := in.Provider + "-local"
		v, err := r.askDefault(out, "Source name", def)
		if err != nil {
			return initInputs{}, err
		}
		in.Name = v
	}
	if in.Port == 0 {
		v, err := r.askDefault(out, "Local port", "3000")
		if err != nil {
			return initInputs{}, err
		}
		p, perr := strconv.Atoi(strings.TrimSpace(v))
		if perr != nil || p <= 0 || p >= 65536 {
			return initInputs{}, fmt.Errorf("invalid port %q", v)
		}
		in.Port = p
	}
	if in.Handler == "" {
		def := detectStack(".")
		v, err := r.choose(out, "Starter handler", supportedHandlers, def)
		if err != nil {
			return initInputs{}, err
		}
		in.Handler = v
	}
	return in, nil
}

// runInitWithInputs is the post-prompt logic shared by both paths:
// create the source, write the config, drop the handler stub.
func runInitWithInputs(ctx context.Context, a *app, c *httpc.Client, in initInputs) error {
	a.stdout.Println(output.None, "")
	a.stdout.Println(output.Muted, "Creating source on Hookwave…")

	created, err := createSource(ctx, c, in.Name, in.Provider)
	if err != nil {
		return err
	}

	if err := writeConfigToml(".hookwave.toml", configToml{
		SourceID:  created.Data.ID,
		Provider:  created.Data.Provider,
		IngestURL: created.IngestURL,
		LocalPort: in.Port,
	}); err != nil {
		return err
	}
	a.stdout.Printf(output.Success, "  ✓ wrote %s\n", ".hookwave.toml")

	if in.Handler != "" && in.Handler != "none" {
		path, werr := writeHandlerStub("handlers", in.Provider, in.Handler, in.Force)
		if werr != nil {
			return werr
		}
		if path != "" {
			a.stdout.Printf(output.Success, "  ✓ wrote %s\n", path)
		} else {
			a.stdout.Printf(output.Muted, "  · handler already exists, left alone\n")
		}
	}

	a.stdout.Println(output.None, "")
	a.stdout.Println(output.None, "Next:")
	a.stdout.Printf(output.None, "  Point your provider at: %s\n", a.stdout.Stylize(output.Success, created.IngestURL))
	a.stdout.Printf(output.None, "  Then run:               %s\n", a.stdout.Stylize(output.Success, "hookwave listen"))
	a.stdout.Println(output.None, "")
	return nil
}

func createSource(ctx context.Context, c *httpc.Client, name, provider string) (*sourceCreateResp, error) {
	var resp sourceCreateResp
	if err := c.Post(ctx, "/v1/sources", sourceCreateBody{Name: name, Provider: provider}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}
