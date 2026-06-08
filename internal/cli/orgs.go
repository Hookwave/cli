package cli

import (
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/hookwave/cli/internal/config"
	"github.com/hookwave/cli/internal/output"
)

type orgsListResp struct {
	Data []struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Plan   string `json:"plan"`
		Role   string `json:"role"`
		Active bool   `json:"active"`
	} `json:"data"`
}

func newOrgsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "orgs",
		Short: "Manage which org the CLI talks to",
	}
	cmd.AddCommand(newOrgsListCmd(), newOrgsUseCmd())
	return cmd
}

func newOrgsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List orgs you belong to",
		RunE: func(cmd *cobra.Command, args []string) error {
			a := appFrom(cmd)
			c, err := a.authedClient()
			if err != nil {
				return err
			}
			var r orgsListResp
			if err := c.Get(cmd.Context(), "/v1/me/orgs", &r); err != nil {
				return err
			}
			tw := tabwriter.NewWriter(&stdoutWriter{a.stdout}, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "  \tID\tNAME\tROLE\tPLAN")
			for _, o := range r.Data {
				active := " "
				if o.ID == a.cfg.ActiveOrg || o.Active {
					active = "*"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", active, o.ID, o.Name, o.Role, o.Plan)
			}
			return tw.Flush()
		},
	}
}

func newOrgsUseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "use <org-id>",
		Short: "Set the active org for subsequent CLI calls",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a := appFrom(cmd)
			id := strings.TrimSpace(args[0])
			if id == "" {
				return fmt.Errorf("org id is empty")
			}
			a.cfg.ActiveOrg = id
			if err := config.Save(a.cfg); err != nil {
				return err
			}
			a.stdout.Printf(output.Success, "Active org set to %s\n", id)
			return nil
		},
	}
}

// stdoutWriter adapts our Printer to io.Writer for tabwriter output.
type stdoutWriter struct{ p *output.Printer }

func (w *stdoutWriter) Write(b []byte) (int, error) {
	w.p.Plain(string(b))
	return len(b), nil
}
