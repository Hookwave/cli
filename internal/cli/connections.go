package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/hookwave/hookwave/apps/cli/internal/output"
)

// CLI surface over /v1/connections. Connections are the most-edited
// entity in production (filters, transformations, retry tweaks), so
// CLI access here is the highest-value of the CRUD wrappers.
//
// `create` supports two ergonomics:
//   - simple flag form: --source / --destination / --name
//   - JSON via stdin: `cat conn.json | hookwave connections create -f -`
// Complex filters / transformations need #2 because they don't fit
// flag syntax cleanly. The same JSON shape works for `update`.

type connectionRow struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	SourceID    string    `json:"sourceId"`
	SourceName  string    `json:"sourceName"`
	DestID      string    `json:"destinationId"`
	DestName    string    `json:"destinationName"`
	Paused      bool      `json:"paused"`
	CreatedAt   time.Time `json:"createdAt"`
}

type connectionsListResp struct {
	Data []connectionRow `json:"data"`
}

func newConnectionsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "connections",
		Aliases: []string{"conn"},
		Short:   "Manage source → destination connections",
	}
	cmd.AddCommand(
		newConnectionsListCmd(),
		newConnectionsGetCmd(),
		newConnectionsCreateCmd(),
		newConnectionsUpdateCmd(),
		newConnectionsDeleteCmd(),
		newConnectionsPauseCmd(),
		newConnectionsUnpauseCmd(),
		newConnectionsUpsertCmd(),
	)
	return cmd
}

func newConnectionsPauseCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "pause <id>",
		Aliases: []string{"disable"},
		Short:   "Pause delivery on a connection",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return setPaused(cmd, "/v1/connections/"+url.PathEscape(args[0]), true)
		},
	}
}

func newConnectionsUnpauseCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "unpause <id>",
		Aliases: []string{"enable", "resume"},
		Short:   "Resume a paused connection",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return setPaused(cmd, "/v1/connections/"+url.PathEscape(args[0]), false)
		},
	}
}

// newConnectionsUpsertCmd does a client-side upsert by name. List
// existing connections, find one with the given name, then PATCH or
// POST. Idempotent enough for IaC; not perfectly atomic, but real
// users have unique names per environment.
func newConnectionsUpsertCmd() *cobra.Command {
	var (
		filePath string
		name     string
	)
	cmd := &cobra.Command{
		Use:   "upsert",
		Short: "Create or update a connection by name (idempotent)",
		Long: `Idempotent create-or-update by name. Pass JSON via -f (or stdin '-')
that includes "name" — we look it up; if found, we PATCH; if not, we POST.

  cat conn.json | hookwave connections upsert -f -
  hookwave connections upsert -f conn.json`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a := appFrom(cmd)
			c, err := a.authedClient()
			if err != nil {
				return err
			}
			if filePath == "" {
				return errors.New("upsert requires -f <file|->")
			}
			b, rerr := readBodyFile(filePath)
			if rerr != nil {
				return rerr
			}
			var body map[string]any
			if jerr := json.Unmarshal(b, &body); jerr != nil {
				return fmt.Errorf("parse JSON: %w", jerr)
			}
			look := name
			if look == "" {
				if v, ok := body["name"].(string); ok {
					look = v
				}
			}
			if look == "" {
				return errors.New("upsert needs --name or a 'name' field in the JSON")
			}

			// Look up existing by name.
			var list connectionsListResp
			if err := c.Get(cmd.Context(), "/v1/connections", &list); err != nil {
				return err
			}
			var existing string
			for _, r := range list.Data {
				if r.Name == look {
					existing = r.ID
					break
				}
			}

			if existing == "" {
				if err := c.Post(cmd.Context(), "/v1/connections", body, nil); err != nil {
					return err
				}
				a.stdout.Printf(output.Success, "✓ Created connection %q\n", look)
			} else {
				if err := c.Do(cmd.Context(), "PATCH", "/v1/connections/"+url.PathEscape(existing), body, nil); err != nil {
					return err
				}
				a.stdout.Printf(output.Success, "✓ Updated connection %q (%s)\n", look, existing)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "look up by this name (overrides JSON 'name')")
	cmd.Flags().StringVarP(&filePath, "file", "f", "", "JSON file path; use '-' for stdin")
	return cmd
}

func newConnectionsListCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List connections in the active org",
		RunE: func(cmd *cobra.Command, _ []string) error {
			a := appFrom(cmd)
			c, err := a.authedClient()
			if err != nil {
				return err
			}
			var r connectionsListResp
			if err := c.Get(cmd.Context(), "/v1/connections", &r); err != nil {
				return err
			}
			if jsonOut {
				return printJSON(a.stdout, r.Data)
			}
			return renderConnectionsTable(a.stdout, r.Data)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON instead of a table")
	return cmd
}

func newConnectionsGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <id>",
		Short: "Print a single connection as JSON",
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
			if err := c.Get(cmd.Context(), "/v1/connections/"+url.PathEscape(args[0]), &r); err != nil {
				return err
			}
			return printJSON(a.stdout, r.Data)
		},
	}
}

func newConnectionsCreateCmd() *cobra.Command {
	var (
		name       string
		srcID      string
		destID     string
		filePath   string
		paused     bool
		templateID string
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a connection",
		Long: `Create a connection wiring a source to a destination.

Simple form:
  hookwave connections create --name stripe-to-prod \
    --source SRC_ID --destination DEST_ID

Complex (filter, transformation, retry tweaks) — pass JSON via stdin:
  cat connection.json | hookwave connections create -f -

JSON shape mirrors the API:
  {
    "name": "stripe-to-prod",
    "sourceId": "...",
    "destinationId": "...",
    "filterExpression": { "all": [{"path": "$.body.type", "op": "eq", "value": "invoice.paid"}] },
    "outboundFormat": "raw",
    "timeoutMs": 30000,
    "maxAttempts": 7
  }`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a := appFrom(cmd)
			c, err := a.authedClient()
			if err != nil {
				return err
			}

			var body map[string]any
			if filePath != "" {
				b, rerr := readBodyFile(filePath)
				if rerr != nil {
					return rerr
				}
				if jerr := json.Unmarshal(b, &body); jerr != nil {
					return fmt.Errorf("parse JSON: %w", jerr)
				}
			} else {
				if name == "" || srcID == "" || destID == "" {
					return errors.New("simple form requires --name, --source, --destination (or pass -f for JSON)")
				}
				body = map[string]any{
					"name":          name,
					"sourceId":      srcID,
					"destinationId": destID,
				}
				if paused {
					body["paused"] = true
				}
			}

			// --template <id> splices a saved template's config into the
			// create body. Existing keys (--name etc.) win — the template
			// only fills in things the user didn't specify.
			if templateID != "" {
				var t struct {
					Data struct {
						Config map[string]any `json:"config"`
					} `json:"data"`
				}
				if err := c.Get(cmd.Context(), "/v1/connection-templates/"+url.PathEscape(templateID), &t); err != nil {
					return fmt.Errorf("fetch template: %w", err)
				}
				for k, v := range t.Data.Config {
					if _, exists := body[k]; !exists {
						body[k] = v
					}
				}
			}
			var r struct {
				Data connectionRow `json:"data"`
			}
			if err := c.Post(cmd.Context(), "/v1/connections", body, &r); err != nil {
				return err
			}
			a.stdout.Printf(output.Success, "✓ Created connection %s (%s)\n", r.Data.ID, r.Data.Name)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "connection name (simple form)")
	cmd.Flags().StringVar(&srcID, "source", "", "source id (simple form)")
	cmd.Flags().StringVar(&destID, "destination", "", "destination id (simple form)")
	cmd.Flags().BoolVar(&paused, "paused", false, "create paused")
	cmd.Flags().StringVarP(&filePath, "file", "f", "", "JSON file path; use '-' for stdin")
	cmd.Flags().StringVar(&templateID, "template", "", "apply a saved template's config (overridden by any matching --flag)")
	return cmd
}

func newConnectionsUpdateCmd() *cobra.Command {
	var (
		filePath string
		name     string
		pauseStr string
	)
	cmd := &cobra.Command{
		Use:   "update <id>",
		Short: "Update fields on a connection",
		Long: `Apply a partial update. Two ways:

  hookwave connections update CONN_ID --name new-name --paused true
  cat patch.json | hookwave connections update CONN_ID -f -

The JSON form supports any field the dashboard exposes (filterExpression,
retryStrategy, outboundFormat, outboundTemplate, headers, etc.).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a := appFrom(cmd)
			c, err := a.authedClient()
			if err != nil {
				return err
			}
			patch := map[string]any{}
			if filePath != "" {
				b, rerr := readBodyFile(filePath)
				if rerr != nil {
					return rerr
				}
				if jerr := json.Unmarshal(b, &patch); jerr != nil {
					return fmt.Errorf("parse JSON: %w", jerr)
				}
			}
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
				return errors.New("no fields to update — pass --name, --paused, or -f patch.json")
			}
			if err := c.Do(cmd.Context(), "PATCH", "/v1/connections/"+url.PathEscape(args[0]), patch, nil); err != nil {
				return err
			}
			a.stdout.Println(output.Success, "✓ Updated.")
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "rename")
	cmd.Flags().StringVar(&pauseStr, "paused", "", "true|false")
	cmd.Flags().StringVarP(&filePath, "file", "f", "", "JSON file path; use '-' for stdin (merged with flag values)")
	return cmd
}

func newConnectionsDeleteCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <id>",
		Short: "Soft-delete a connection",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a := appFrom(cmd)
			if !force {
				if ok, err := confirm(a.stdout, fmt.Sprintf("Delete connection %s?", args[0])); err != nil || !ok {
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
			if err := c.Delete(cmd.Context(), "/v1/connections/"+url.PathEscape(args[0])); err != nil {
				return err
			}
			a.stdout.Println(output.Success, "✓ Deleted.")
			return nil
		},
	}
	cmd.Flags().BoolVarP(&force, "force", "f", false, "skip confirmation prompt")
	return cmd
}

func renderConnectionsTable(p *output.Printer, rows []connectionRow) error {
	if len(rows) == 0 {
		p.Println(output.Muted, "(no connections)")
		return nil
	}
	tw := tabwriter.NewWriter(&stdoutWriter{p}, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tSOURCE\tDESTINATION\tSTATE")
	for _, r := range rows {
		state := "active"
		if r.Paused {
			state = p.Stylize(output.Warn, "paused")
		}
		src := r.SourceName
		if src == "" {
			src = r.SourceID
		}
		dst := r.DestName
		if dst == "" {
			dst = r.DestID
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			truncate(r.ID, 12),
			truncate(r.Name, 24),
			truncate(src, 24),
			truncate(dst, 24),
			state,
		)
	}
	return tw.Flush()
}

// readBodyFile reads JSON from a path or stdin (`-`).
func readBodyFile(path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(io.LimitReader(os.Stdin, 4*1024*1024))
	}
	return os.ReadFile(path)
}
