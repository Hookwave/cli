package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hookwave/hookwave/apps/cli/internal/output"
)

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect CLI configuration",
	}
	cmd.AddCommand(newConfigPathCmd(), newConfigShowCmd())
	return cmd
}

func newConfigPathCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "Print the resolved config file path",
		RunE: func(cmd *cobra.Command, _ []string) error {
			a := appFrom(cmd)
			a.stdout.Printf(output.None, "%s\n", configPath())
			return nil
		},
	}
}

func newConfigShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Print the resolved config (no secrets)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			a := appFrom(cmd)
			a.stdout.Printf(output.None, "API base:    %s\n", a.cfg.ResolvedAPIBase())
			a.stdout.Printf(output.None, "Active org:  %s\n", emptyDash(a.cfg.ActiveOrg))
			a.stdout.Printf(output.None, "Signed in:   %s\n", emptyDash(a.cfg.UserEmail))
			return nil
		},
	}
}

func emptyDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func configPath() string {
	// Mirror config.dir + "config.json" without exporting it (the
	// config package keeps its lookup logic private).
	return fmt.Sprintf("$XDG_CONFIG_HOME/hookwave/config.json (or ~/.config/hookwave/config.json)")
}
