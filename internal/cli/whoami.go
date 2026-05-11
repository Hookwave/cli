package cli

import (
	"github.com/spf13/cobra"

	"github.com/hookwave/hookwave/apps/cli/internal/output"
)

type whoamiResp struct {
	Data struct {
		User struct {
			ID    string `json:"id"`
			Email string `json:"email"`
		} `json:"user"`
		Org struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"org"`
	} `json:"data"`
}

func newWhoamiCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Show the signed-in user and active org",
		RunE: func(cmd *cobra.Command, args []string) error {
			a := appFrom(cmd)
			c, err := a.authedClient()
			if err != nil {
				return err
			}
			var r whoamiResp
			if err := c.Get(cmd.Context(), "/v1/me", &r); err != nil {
				return err
			}
			a.stdout.Printf(output.None, "User: %s\nOrg:  %s (%s)\n",
				r.Data.User.Email, r.Data.Org.Name, r.Data.Org.ID)
			return nil
		},
	}
}
