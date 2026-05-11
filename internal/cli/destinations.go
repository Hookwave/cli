package cli

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/hookwave/hookwave/apps/cli/internal/httpc"
	"github.com/hookwave/hookwave/apps/cli/internal/output"
)

// CLI surface over /v1/destinations. Wraps existing endpoints — no
// new server logic. Designed for IaC / scripting; humans setting up
// a destination once typically use the dashboard.

type destinationRow struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	DestinationType string    `json:"destinationType"`
	DestinationURL  string    `json:"destinationUrl"`
	Paused          bool      `json:"paused"`
	CreatedAt       time.Time `json:"createdAt"`
}

type destinationsListResp struct {
	Data []destinationRow `json:"data"`
}

type destinationCreateBody struct {
	Name            string `json:"name"`
	DestinationType string `json:"destinationType"`
	DestinationURL  string `json:"destinationUrl,omitempty"`
	Paused          *bool  `json:"paused,omitempty"`
	// Outbound auth — secret travels in plaintext over TLS, encrypted
	// server-side before persistence. Pass empty AuthKind for none.
	AuthKind       string `json:"authKind,omitempty"`
	AuthSecret     string `json:"authSecret,omitempty"`
	AuthHeaderName string `json:"authHeaderName,omitempty"`
}

var supportedDestinationTypes = []string{"http", "n8n", "make", "slack", "discord", "mock"}
var supportedAuthKinds = []string{"none", "bearer", "api_key", "basic"}

func newDestinationsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "destinations",
		Aliases: []string{"dest"},
		Short:   "Manage outbound delivery targets (HTTP, Slack, Discord, ...)",
	}
	cmd.AddCommand(
		newDestinationsListCmd(),
		newDestinationsGetCmd(),
		newDestinationsCreateCmd(),
		newDestinationsUpdateCmd(),
		newDestinationsDeleteCmd(),
		newDestinationsPauseCmd(),
		newDestinationsUnpauseCmd(),
		newDestinationsUpsertCmd(),
	)
	return cmd
}

func newDestinationsPauseCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "pause <id>",
		Aliases: []string{"disable"},
		Short:   "Pause a destination",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return setPaused(cmd, "/v1/destinations/"+url.PathEscape(args[0]), true)
		},
	}
}

func newDestinationsUnpauseCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "unpause <id>",
		Aliases: []string{"enable", "resume"},
		Short:   "Resume a paused destination",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return setPaused(cmd, "/v1/destinations/"+url.PathEscape(args[0]), false)
		},
	}
}

func newDestinationsUpsertCmd() *cobra.Command {
	var (
		name     string
		destType string
		destURL  string
	)
	cmd := &cobra.Command{
		Use:   "upsert",
		Short: "Create or update a destination by name (idempotent)",
		Long: `Idempotent create-or-update by name. Looks up an existing destination
matching --name; if found, applies a PATCH; if not, POSTs a create.

  hookwave destinations upsert --name prod-api --type http --url https://api.acme.com/hooks`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a := appFrom(cmd)
			c, err := a.authedClient()
			if err != nil {
				return err
			}
			if name == "" {
				return errors.New("--name is required")
			}
			var list destinationsListResp
			if err := c.Get(cmd.Context(), "/v1/destinations", &list); err != nil {
				return err
			}
			var existing string
			for _, d := range list.Data {
				if d.Name == name {
					existing = d.ID
					break
				}
			}
			if existing == "" {
				if destType == "" {
					return errors.New("create branch needs --type")
				}
				if !contains(supportedDestinationTypes, destType) {
					return fmt.Errorf("--type %q not supported", destType)
				}
				body := destinationCreateBody{Name: name, DestinationType: destType, DestinationURL: destURL}
				if err := c.Post(cmd.Context(), "/v1/destinations", body, nil); err != nil {
					return err
				}
				a.stdout.Printf(output.Success, "✓ Created destination %q\n", name)
				return nil
			}
			patch := map[string]any{}
			if destURL != "" {
				patch["destinationUrl"] = destURL
			}
			if destType != "" {
				patch["destinationType"] = destType
			}
			if len(patch) == 0 {
				a.stdout.Printf(output.Muted, "destination %q exists with no changes\n", name)
				return nil
			}
			if err := c.Do(cmd.Context(), "PATCH", "/v1/destinations/"+url.PathEscape(existing), patch, nil); err != nil {
				return err
			}
			a.stdout.Printf(output.Success, "✓ Updated destination %q (%s)\n", name, existing)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "destination name (lookup key)")
	cmd.Flags().StringVar(&destType, "type", "", "destination type (required on create)")
	cmd.Flags().StringVar(&destURL, "url", "", "destination URL")
	return cmd
}

func newDestinationsListCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List destinations in the active org",
		RunE: func(cmd *cobra.Command, _ []string) error {
			a := appFrom(cmd)
			c, err := a.authedClient()
			if err != nil {
				return err
			}
			var r destinationsListResp
			if err := c.Get(cmd.Context(), "/v1/destinations", &r); err != nil {
				return err
			}
			if jsonOut {
				return printJSON(a.stdout, r.Data)
			}
			return renderDestinationsTable(a.stdout, r.Data)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON instead of a table")
	return cmd
}

func newDestinationsGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <id>",
		Short: "Print a single destination as JSON",
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
			if err := c.Get(cmd.Context(), "/v1/destinations/"+url.PathEscape(args[0]), &r); err != nil {
				return err
			}
			return printJSON(a.stdout, r.Data)
		},
	}
}

func newDestinationsCreateCmd() *cobra.Command {
	var (
		name           string
		destType       string
		destURL        string
		paused         bool
		authKind       string
		authSecret     string
		authHeaderName string
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a destination",
		Long: `Create an outbound delivery target. Examples:

  hookwave destinations create --name prod-api --type http --url https://api.acme.com/webhooks
  hookwave destinations create --name prod --type http --url ... --auth bearer --auth-secret $TOKEN
  hookwave destinations create --name slack-alerts --type slack --url https://hooks.slack.com/services/...`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a := appFrom(cmd)
			if name == "" {
				return errors.New("--name is required")
			}
			if destType == "" {
				return errors.New("--type is required")
			}
			if !contains(supportedDestinationTypes, destType) {
				return fmt.Errorf("--type %q not supported (one of: %s)", destType, strings.Join(supportedDestinationTypes, ", "))
			}
			if destType != "mock" && destURL == "" {
				return errors.New("--url is required unless --type is mock")
			}
			if authKind != "" && !contains(supportedAuthKinds, authKind) {
				return fmt.Errorf("--auth %q not supported (one of: %s)", authKind, strings.Join(supportedAuthKinds, ", "))
			}
			if authKind != "" && authKind != "none" && authSecret == "" {
				return errors.New("--auth-secret is required when --auth is bearer / api_key / basic")
			}
			c, err := a.authedClient()
			if err != nil {
				return err
			}
			body := destinationCreateBody{Name: name, DestinationType: destType, DestinationURL: destURL}
			if paused {
				v := true
				body.Paused = &v
			}
			if authKind != "" {
				body.AuthKind = authKind
				body.AuthSecret = authSecret
				body.AuthHeaderName = authHeaderName
			}
			var r struct {
				Data destinationRow `json:"data"`
			}
			if err := c.Post(cmd.Context(), "/v1/destinations", body, &r); err != nil {
				return err
			}
			a.stdout.Printf(output.Success, "✓ Created destination %s (%s)\n", r.Data.ID, r.Data.Name)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "human-readable name")
	cmd.Flags().StringVar(&destType, "type", "http", "destination type: http|n8n|make|slack|discord|mock")
	cmd.Flags().StringVar(&destURL, "url", "", "destination URL (required unless --type=mock)")
	cmd.Flags().BoolVar(&paused, "paused", false, "create paused")
	cmd.Flags().StringVar(&authKind, "auth", "", "outbound auth: none|bearer|api_key|basic")
	cmd.Flags().StringVar(&authSecret, "auth-secret", "", "secret for the chosen auth (token / key / 'user:pass')")
	cmd.Flags().StringVar(&authHeaderName, "auth-header", "", "header name for --auth=api_key (default: X-API-Key)")
	return cmd
}

func newDestinationsUpdateCmd() *cobra.Command {
	var (
		name     string
		destURL  string
		pauseStr string
	)
	cmd := &cobra.Command{
		Use:   "update <id>",
		Short: "Update one or more fields on a destination",
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
			if destURL != "" {
				patch["destinationUrl"] = destURL
			}
			switch strings.ToLower(strings.TrimSpace(pauseStr)) {
			case "true", "yes", "on":
				patch["paused"] = true
			case "false", "no", "off":
				patch["paused"] = false
			case "":
				// not provided
			default:
				return fmt.Errorf("--paused must be true/false, got %q", pauseStr)
			}
			if len(patch) == 0 {
				return errors.New("no fields to update — pass --name, --url, or --paused")
			}
			if err := c.Do(cmd.Context(), "PATCH", "/v1/destinations/"+url.PathEscape(args[0]), patch, nil); err != nil {
				return err
			}
			a.stdout.Println(output.Success, "✓ Updated.")
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "rename")
	cmd.Flags().StringVar(&destURL, "url", "", "change the destination URL")
	cmd.Flags().StringVar(&pauseStr, "paused", "", "true|false — pause or resume")
	return cmd
}

func newDestinationsDeleteCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <id>",
		Short: "Soft-delete a destination",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a := appFrom(cmd)
			if !force {
				if ok, err := confirm(a.stdout, fmt.Sprintf("Delete destination %s? (existing connections will fail to deliver)", args[0])); err != nil || !ok {
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
			if err := c.Delete(cmd.Context(), "/v1/destinations/"+url.PathEscape(args[0])); err != nil {
				return err
			}
			a.stdout.Println(output.Success, "✓ Deleted.")
			return nil
		},
	}
	cmd.Flags().BoolVarP(&force, "force", "f", false, "skip confirmation prompt")
	return cmd
}

func renderDestinationsTable(p *output.Printer, rows []destinationRow) error {
	if len(rows) == 0 {
		p.Println(output.Muted, "(no destinations)")
		return nil
	}
	tw := tabwriter.NewWriter(&stdoutWriter{p}, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tTYPE\tURL\tSTATE")
	for _, r := range rows {
		state := "active"
		if r.Paused {
			state = p.Stylize(output.Warn, "paused")
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			truncate(r.ID, 12),
			truncate(r.Name, 28),
			r.DestinationType,
			truncate(r.DestinationURL, 50),
			state,
		)
	}
	return tw.Flush()
}

// confirm prints a yes/no prompt and returns true on y/yes.
func confirm(out *output.Printer, question string) (bool, error) {
	r := newPromptReader()
	out.Printf(output.Warn, "%s [y/N]: ", question)
	line, err := r.readLine()
	if err != nil {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	}
	return false, nil
}

// suppress unused warnings until shared.
var _ = context.Canceled
var _ httpc.APIError
