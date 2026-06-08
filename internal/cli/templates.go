package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/hookwave/cli/internal/output"
)

// CLI surface over /v1/connection-templates. Templates capture a
// connection's full per-pair config (filter / retry / transform /
// headers / rate limit) so a team can spin up many similar
// connections with one click. Apply happens client-side: the user
// fetches a template, splices it into a connection-create body, and
// posts to /v1/connections.

type templateRow struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Description *string        `json:"description"`
	Config      map[string]any `json:"config"`
	CreatedAt   time.Time      `json:"createdAt"`
}

type templatesListResp struct {
	Data []templateRow `json:"data"`
}

func newTemplatesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "templates",
		Aliases: []string{"tmpl"},
		Short:   "Manage saved connection templates",
		Long: `Connection templates capture filter / retry / transform / headers /
rate limit so you can spin up similar connections with one click.

Apply a template via:

  hookwave connections create --name new --source SRC --destination DEST \
      --template <template-id>`,
	}
	cmd.AddCommand(
		newTemplatesListCmd(),
		newTemplatesGetCmd(),
		newTemplatesCreateCmd(),
		newTemplatesDeleteCmd(),
	)
	return cmd
}

func newTemplatesListCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List connection templates in the active org",
		RunE: func(cmd *cobra.Command, _ []string) error {
			a := appFrom(cmd)
			c, err := a.authedClient()
			if err != nil {
				return err
			}
			var r templatesListResp
			if err := c.Get(cmd.Context(), "/v1/connection-templates", &r); err != nil {
				return err
			}
			if jsonOut {
				return printJSON(a.stdout, r.Data)
			}
			if len(r.Data) == 0 {
				a.stdout.Println(output.Muted, "(no templates)")
				return nil
			}
			tw := tabwriter.NewWriter(&stdoutWriter{a.stdout}, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tNAME\tDESCRIPTION\tCREATED")
			for _, t := range r.Data {
				desc := ""
				if t.Description != nil {
					desc = *t.Description
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
					truncate(t.ID, 12),
					truncate(t.Name, 28),
					truncate(desc, 36),
					t.CreatedAt.Local().Format("2006-01-02 15:04"),
				)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON instead of a table")
	return cmd
}

func newTemplatesGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <id>",
		Short: "Print a template (full config) as JSON",
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
			if err := c.Get(cmd.Context(), "/v1/connection-templates/"+url.PathEscape(args[0]), &r); err != nil {
				return err
			}
			return printJSON(a.stdout, r.Data)
		},
	}
}

func newTemplatesCreateCmd() *cobra.Command {
	var (
		name        string
		description string
		fromConn    string
		filePath    string
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a template — capture a connection's config or supply JSON inline",
		Long: `Two ways to create a template:

  # Capture an existing connection's config
  hookwave templates create --name "stripe-rules" --from <connection-id>

  # Supply config JSON directly (same shape as the connection PATCH body)
  cat config.json | hookwave templates create --name "stripe-rules" -f -`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a := appFrom(cmd)
			if name == "" {
				return errors.New("--name is required")
			}
			if fromConn == "" && filePath == "" {
				return errors.New("either --from <connection-id> or -f <file> is required")
			}
			c, err := a.authedClient()
			if err != nil {
				return err
			}
			body := map[string]any{"name": name}
			if description != "" {
				body["description"] = description
			}
			if fromConn != "" {
				body["fromConnectionId"] = fromConn
			}
			if filePath != "" {
				b, rerr := readBodyFile(filePath)
				if rerr != nil {
					return rerr
				}
				var cfg map[string]any
				if jerr := json.Unmarshal(b, &cfg); jerr != nil {
					return fmt.Errorf("parse config JSON: %w", jerr)
				}
				body["config"] = cfg
			}
			var r struct {
				Data templateRow `json:"data"`
			}
			if err := c.Post(cmd.Context(), "/v1/connection-templates", body, &r); err != nil {
				return err
			}
			a.stdout.Printf(output.Success, "✓ Created template %s (%s)\n", r.Data.ID, r.Data.Name)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "template name (required)")
	cmd.Flags().StringVar(&description, "description", "", "human-readable description")
	cmd.Flags().StringVar(&fromConn, "from", "", "capture this connection's config")
	cmd.Flags().StringVarP(&filePath, "file", "f", "", "JSON file (use '-' for stdin) — same shape as the connection PATCH body")
	return cmd
}

func newTemplatesDeleteCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <id>",
		Short: "Soft-delete a template",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a := appFrom(cmd)
			if !force {
				if ok, err := confirm(a.stdout, fmt.Sprintf("Delete template %s?", args[0])); err != nil || !ok {
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
			if err := c.Delete(cmd.Context(), "/v1/connection-templates/"+url.PathEscape(args[0])); err != nil {
				return err
			}
			a.stdout.Println(output.Success, "✓ Deleted.")
			return nil
		},
	}
	cmd.Flags().BoolVarP(&force, "force", "f", false, "skip confirmation prompt")
	return cmd
}
