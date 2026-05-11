package cli

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
)

// promptInitTUI renders an interactive Bubble Tea form via huh and
// returns the user's choices. The form pre-fills any value already
// provided in `seed` (from CLI flags or stack detection).
func promptInitTUI(seed initInputs) (initInputs, error) {
	in := seed

	// Defaults that show up in the form before the user touches it.
	if in.Provider == "" {
		in.Provider = "generic"
	}
	if in.Handler == "" {
		in.Handler = "none"
	}
	defaultName := in.Name
	if defaultName == "" {
		defaultName = in.Provider + "-local"
	}
	defaultPort := ""
	if in.Port > 0 {
		defaultPort = strconv.Itoa(in.Port)
	} else {
		defaultPort = "3000"
	}

	// We bind temporary string vars and parse the port at the end so
	// huh's validation can stay synchronous and simple.
	provider := in.Provider
	name := defaultName
	port := defaultPort
	handler := in.Handler

	// Each field goes in its own Group so huh renders one step per
	// screen — Enter (or Tab on inputs) advances to the next step.
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Provider").
				Description("Used to pick the right inbound signature verifier.").
				Options(
					huh.NewOption("Stripe", "stripe"),
					huh.NewOption("Shopify", "shopify"),
					huh.NewOption("GitHub", "github"),
					huh.NewOption("Replicate", "replicate"),
					huh.NewOption("Lemon Squeezy", "lemonsqueezy"),
					huh.NewOption("Generic (Hookwave HMAC)", "generic"),
				).
				Value(&provider),
		),
		huh.NewGroup(
			huh.NewInput().
				Title("Source name").
				Description("Shown in the dashboard. Free-form.").
				Value(&name).
				Validate(func(s string) error {
					s = strings.TrimSpace(s)
					if s == "" {
						return errors.New("name is required")
					}
					if len(s) > 200 {
						return errors.New("name must be 200 chars or fewer")
					}
					return nil
				}),
		),
		huh.NewGroup(
			huh.NewInput().
				Title("Local port").
				Description("`hookwave listen` will forward webhooks to http://localhost:<port>.").
				Value(&port).
				Validate(func(s string) error {
					p, err := strconv.Atoi(strings.TrimSpace(s))
					if err != nil {
						return errors.New("must be a number")
					}
					if p <= 0 || p >= 65536 {
						return errors.New("port must be 1-65535")
					}
					return nil
				}),
		),
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Starter handler").
				Description("We'll drop a stub at handlers/<provider>.<ext>.").
				Options(
					huh.NewOption("TypeScript", "typescript"),
					huh.NewOption("JavaScript", "javascript"),
					huh.NewOption("Python", "python"),
					huh.NewOption("Go", "go"),
					huh.NewOption("None — I'll write my own", "none"),
				).
				Value(&handler),
		),
	).WithTheme(huhTheme()).WithShowHelp(true)

	if err := form.Run(); err != nil {
		// huh returns user-aborted (Ctrl-C) as an error too. Treat it
		// uniformly — the caller already exits non-zero on error.
		return initInputs{}, err
	}

	p, _ := strconv.Atoi(strings.TrimSpace(port))
	in.Provider = provider
	in.Name = strings.TrimSpace(name)
	in.Port = p
	in.Handler = handler
	return in, nil
}

// huhTheme picks colours that fit the rest of the CLI's TUI (purple
// accent matching the brand, dim muted text). Falls back gracefully
// in lower-color terminals because lipgloss handles that for us.
func huhTheme() *huh.Theme {
	t := huh.ThemeBase()
	primary := lipgloss.Color("#a78bfa") // same purple as listen TUI header
	muted := lipgloss.Color("241")
	t.Focused.Title = t.Focused.Title.Foreground(primary).Bold(true)
	t.Focused.SelectedOption = t.Focused.SelectedOption.Foreground(primary)
	t.Focused.SelectSelector = t.Focused.SelectSelector.Foreground(primary)
	t.Focused.Description = t.Focused.Description.Foreground(muted)
	t.Help.Ellipsis = t.Help.Ellipsis.Foreground(muted)
	return t
}

// Suppress unused-import warnings until we actually want them.
var _ = fmt.Sprintf
