package cli

import (
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/hookwave/cli/internal/output"
)

type tokenRow struct {
	ID         string     `json:"id"`
	ClientName string     `json:"clientName"`
	OrgID      string     `json:"orgId"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
}

type tokensListResp struct {
	Data []tokenRow `json:"data"`
}

func newTokensCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tokens",
		Short: "Manage CLI tokens issued to your user",
		Long: `Lists and revokes the long-lived tokens minted by ` + "`" + `hookwave login` + "`" + `.
Each ` + "`" + `hookwave login` + "`" + ` on a new machine creates a new token; revoke them
here when you sign out of a machine you no longer have.`,
	}
	cmd.AddCommand(newTokensListCmd(), newTokensRevokeCmd())
	return cmd
}

func newTokensListCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List active CLI tokens for your user",
		RunE: func(cmd *cobra.Command, _ []string) error {
			a := appFrom(cmd)
			c, err := a.authedClient()
			if err != nil {
				return err
			}
			var r tokensListResp
			if err := c.Get(cmd.Context(), "/v1/cli/tokens", &r); err != nil {
				return err
			}
			if jsonOut {
				return printJSON(a.stdout, r.Data)
			}
			if len(r.Data) == 0 {
				a.stdout.Println(output.Muted, "(no active tokens)")
				return nil
			}
			tw := tabwriter.NewWriter(&stdoutWriter{a.stdout}, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tCLIENT\tORG\tCREATED\tLAST USED")
			for _, t := range r.Data {
				last := "-"
				if t.LastUsedAt != nil {
					last = t.LastUsedAt.Local().Format("2006-01-02 15:04")
				}
				client := t.ClientName
				if client == "" {
					client = "-"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
					truncate(t.ID, 12),
					truncate(client, 30),
					truncate(t.OrgID, 12),
					t.CreatedAt.Local().Format("2006-01-02 15:04"),
					last,
				)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON instead of a table")
	return cmd
}

func newTokensRevokeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "revoke <token-id>",
		Short: "Revoke a CLI token by id",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a := appFrom(cmd)
			c, err := a.authedClient()
			if err != nil {
				return err
			}
			if err := c.Delete(cmd.Context(), "/v1/cli/tokens/"+args[0]); err != nil {
				return err
			}
			a.stdout.Println(output.Success, "Revoked.")
			return nil
		},
	}
}
