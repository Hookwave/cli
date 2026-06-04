package cli

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/hookwave/hookwave/apps/cli/internal/output"
)

// CLI surface over /v1/sources. `init` scaffolds a starter handler
// directory; `create` is the explicit non-scaffolding way to mint a
// new source from the terminal (CI scripts, scripted provisioning,
// power users who don't want a directory scaffolded).

type sourceRow struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Provider  string    `json:"provider"`
	Paused    bool      `json:"paused"`
	CreatedAt time.Time `json:"createdAt"`
}

type sourcesListResp struct {
	Data []sourceRow `json:"data"`
}

func newSourcesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "sources",
		Aliases: []string{"src"},
		Short:   "Manage inbound webhook sources",
	}
	cmd.AddCommand(
		newSourcesListCmd(),
		newSourcesGetCmd(),
		newSourcesCreateCmd(),
		newSourcesUpdateCmd(),
		newSourcesDeleteCmd(),
		newSourcesPauseCmd(),
		newSourcesUnpauseCmd(),
	)
	return cmd
}

func newSourcesCreateCmd() *cobra.Command {
	var (
		name     string
		provider string
		jsonOut  bool
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create an inbound webhook source",
		Long: `Create a new source. Returns the source row including the ingest
URL you paste into the provider's webhook configuration.

Examples:
  hookwave sources create --name prod-stripe --provider stripe
  hookwave sources create --name my-svc --provider generic --json | jq '.ingestUrl'`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a := appFrom(cmd)
			if name == "" {
				return errors.New("--name is required")
			}
			if provider == "" {
				return errors.New("--provider is required (one of: stripe, shopify, github, replicate, lemonsqueezy, twilio, generic, schedule)")
			}
			c, err := a.authedClient()
			if err != nil {
				return err
			}
			body := map[string]any{"name": name, "provider": provider}
			var r struct {
				Data map[string]any `json:"data"`
			}
			if err := c.Post(cmd.Context(), "/v1/sources", body, &r); err != nil {
				return err
			}
			if jsonOut {
				return printJSON(a.stdout, r.Data)
			}
			id, _ := r.Data["id"].(string)
			ingestURL, _ := r.Data["ingestUrl"].(string)
			a.stdout.Printf(output.Success, "✓ Created source %s (%s)\n", id, name)
			if ingestURL != "" {
				a.stdout.Printf(output.None, "  ingest: %s\n", ingestURL)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "human-readable source name (required)")
	cmd.Flags().StringVar(&provider, "provider", "", "provider verifier: stripe|shopify|github|replicate|lemonsqueezy|twilio|generic|schedule (required)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit the created source row as JSON")
	return cmd
}

func newSourcesListCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List sources in the active org",
		RunE: func(cmd *cobra.Command, _ []string) error {
			a := appFrom(cmd)
			c, err := a.authedClient()
			if err != nil {
				return err
			}
			var r sourcesListResp
			if err := c.Get(cmd.Context(), "/v1/sources", &r); err != nil {
				return err
			}
			if jsonOut {
				return printJSON(a.stdout, r.Data)
			}
			if len(r.Data) == 0 {
				a.stdout.Println(output.Muted, "(no sources)")
				return nil
			}
			tw := tabwriter.NewWriter(&stdoutWriter{a.stdout}, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tNAME\tPROVIDER\tSTATE")
			for _, s := range r.Data {
				state := "active"
				if s.Paused {
					state = a.stdout.Stylize(output.Warn, "paused")
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
					truncate(s.ID, 12),
					truncate(s.Name, 32),
					s.Provider,
					state,
				)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON instead of a table")
	return cmd
}

func newSourcesGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <id>",
		Short: "Print a single source as JSON",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a := appFrom(cmd)
			c, err := a.authedClient()
			if err != nil {
				return err
			}
			var r struct {
				Data map[string]any `json:"data"`
			}
			if err := c.Get(cmd.Context(), "/v1/sources/"+url.PathEscape(args[0]), &r); err != nil {
				return err
			}
			return printJSON(a.stdout, r.Data)
		},
	}
}

func newSourcesUpdateCmd() *cobra.Command {
	var (
		name     string
		pauseStr string
	)
	cmd := &cobra.Command{
		Use:   "update <id>",
		Short: "Update a source's name or paused state",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a := appFrom(cmd)
			c, err := a.authedClient()
			if err != nil {
				return err
			}
			patch := map[string]any{}
			if name != "" {
				patch["name"] = name
			}
			switch strings.ToLower(strings.TrimSpace(pauseStr)) {
			case "true", "yes", "on":
				patch["paused"] = true
			case "false", "no", "off":
				patch["paused"] = false
			case "":
			default:
				return fmt.Errorf("--paused must be true/false, got %q", pauseStr)
			}
			if len(patch) == 0 {
				return errors.New("no fields to update — pass --name or --paused")
			}
			if err := c.Do(cmd.Context(), "PATCH", "/v1/sources/"+url.PathEscape(args[0]), patch, nil); err != nil {
				return err
			}
			a.stdout.Println(output.Success, "✓ Updated.")
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "rename")
	cmd.Flags().StringVar(&pauseStr, "paused", "", "true|false")
	return cmd
}

func newSourcesDeleteCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <id>",
		Short: "Soft-delete a source",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a := appFrom(cmd)
			if !force {
				if ok, err := confirm(a.stdout, fmt.Sprintf("Delete source %s? (downstream connections will fail)", args[0])); err != nil || !ok {
					if err != nil {
						return err
					}
					a.stdout.Println(output.Muted, "aborted.")
					return nil
				}
			}
			c, err := a.authedClient()
			if err != nil {
				return err
			}
			if err := c.Delete(cmd.Context(), "/v1/sources/"+url.PathEscape(args[0])); err != nil {
				return err
			}
			a.stdout.Println(output.Success, "✓ Deleted.")
			return nil
		},
	}
	cmd.Flags().BoolVarP(&force, "force", "f", false, "skip confirmation prompt")
	return cmd
}

func newSourcesPauseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pause <id>",
		Short: "Pause a source (alias for `update --paused true`)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return setPaused(cmd, "/v1/sources/"+url.PathEscape(args[0]), true)
		},
	}
}

func newSourcesUnpauseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unpause <id>",
		Short: "Resume a paused source",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return setPaused(cmd, "/v1/sources/"+url.PathEscape(args[0]), false)
		},
	}
}

// setPaused is the shared body of the lifecycle shortcuts. Used by
// sources/connections/destinations.
func setPaused(cmd *cobra.Command, path string, paused bool) error {
	a := appFrom(cmd)
	c, err := a.authedClient()
	if err != nil {
		return err
	}
	if err := c.Do(cmd.Context(), "PATCH", path, map[string]any{"paused": paused}, nil); err != nil {
		return err
	}
	if paused {
		a.stdout.Println(output.Warn, "✓ Paused.")
	} else {
		a.stdout.Println(output.Success, "✓ Resumed.")
	}
	return nil
}
