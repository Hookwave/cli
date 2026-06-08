package cli

import (
	"github.com/spf13/cobra"

	"github.com/hookwave/cli/internal/auth"
	"github.com/hookwave/cli/internal/config"
	"github.com/hookwave/cli/internal/output"
)

func newLogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Forget the stored CLI token",
		RunE: func(cmd *cobra.Command, args []string) error {
			a := appFrom(cmd)
			if err := auth.DeleteToken(); err != nil {
				return err
			}
			a.cfg.UserID = ""
			a.cfg.UserEmail = ""
			a.cfg.ActiveOrg = ""
			if err := config.Save(a.cfg); err != nil {
				return err
			}
			a.stdout.Println(output.Success, "Signed out.")
			return nil
		},
	}
}
