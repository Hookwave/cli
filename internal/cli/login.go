package cli

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"github.com/hookwave/hookwave/apps/cli/internal/auth"
	"github.com/hookwave/hookwave/apps/cli/internal/config"
	"github.com/hookwave/hookwave/apps/cli/internal/httpc"
	"github.com/hookwave/hookwave/apps/cli/internal/output"
)

// OAuth 2.0 Device Authorization Grant (RFC 8628). The CLI starts a
// flow, gets a short user_code + verification URL, opens the browser,
// then polls for a token until the user approves.

type deviceStartResp struct {
	Data struct {
		DeviceCode      string `json:"deviceCode"`
		UserCode        string `json:"userCode"`
		VerificationURI string `json:"verificationUri"`
		ExpiresIn       int    `json:"expiresIn"`
		Interval        int    `json:"interval"`
	} `json:"data"`
}

type devicePollResp struct {
	Data struct {
		Status      string `json:"status"` // "pending" | "approved" | "expired"
		AccessToken string `json:"accessToken,omitempty"`
		User        struct {
			ID    string `json:"id"`
			Email string `json:"email"`
		} `json:"user,omitempty"`
		Org struct {
			ID string `json:"id"`
		} `json:"org,omitempty"`
	} `json:"data"`
}

func newLoginCmd() *cobra.Command {
	var noBrowser bool
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Authenticate the CLI with Hookwave",
		Long:  "Opens your browser to approve the CLI session. Tokens are stored in your OS keychain.",
		RunE: func(cmd *cobra.Command, args []string) error {
			a := appFrom(cmd)
			return runLogin(cmd.Context(), a, noBrowser)
		},
	}
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "don't try to open the browser; print the URL instead")
	return cmd
}

func runLogin(ctx context.Context, a *app, noBrowser bool) error {
	pub := a.publicClient()

	var start deviceStartResp
	if err := pub.Post(ctx, "/v1/cli/auth/device", map[string]string{
		"clientName": fmt.Sprintf("hookwave-cli/%s on %s/%s", a.build.Version, runtime.GOOS, runtime.GOARCH),
	}, &start); err != nil {
		return fmt.Errorf("start device flow: %w", err)
	}

	verify := start.Data.VerificationURI
	if verify == "" {
		return errors.New("server returned no verification URI")
	}

	a.stdout.Println(output.None, "")
	a.stdout.Printf(output.None, "  Visit:        %s\n", a.stdout.Stylize(output.Success, verify))
	a.stdout.Printf(output.None, "  Confirm code: %s\n", a.stdout.Stylize(output.Success, start.Data.UserCode))
	a.stdout.Println(output.None, "")
	a.stdout.Println(output.Muted, "  (Code is shown so you can verify the page is talking to your CLI.)")
	a.stdout.Println(output.None, "")

	if !noBrowser {
		if err := openBrowser(verify); err != nil {
			a.stdout.Println(output.Muted, "  Could not open browser automatically — paste the URL above.")
		}
	}

	a.stdout.Println(output.Muted, "  Waiting for approval...")

	tok, userID, userEmail, orgID, err := pollForToken(ctx, pub, start.Data.DeviceCode, time.Duration(start.Data.Interval)*time.Second, time.Duration(start.Data.ExpiresIn)*time.Second)
	if err != nil {
		return err
	}

	if err := auth.SetToken(tok); err != nil {
		return fmt.Errorf("store token: %w", err)
	}

	a.cfg.UserID = userID
	a.cfg.UserEmail = userEmail
	if a.cfg.ActiveOrg == "" {
		a.cfg.ActiveOrg = orgID
	}
	if err := config.Save(a.cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	a.stdout.Println(output.None, "")
	a.stdout.Printf(output.Success, "  ✓ Signed in as %s\n", userEmail)
	return nil
}

func pollForToken(ctx context.Context, c *httpc.Client, deviceCode string, interval, total time.Duration) (token, userID, userEmail, orgID string, err error) {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	if total <= 0 {
		total = 10 * time.Minute
	}
	deadline := time.Now().Add(total)
	t := time.NewTicker(interval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return "", "", "", "", ctx.Err()
		case <-t.C:
			if time.Now().After(deadline) {
				return "", "", "", "", errors.New("login timed out — try `hookwave login` again")
			}
			var p devicePollResp
			perr := c.Post(ctx, "/v1/cli/auth/device/token", map[string]string{"deviceCode": deviceCode}, &p)
			if perr != nil {
				// 410 means the device code is gone (expired or already used).
				if ae, ok := perr.(*httpc.APIError); ok && (ae.Status == 404 || ae.Status == 410) {
					return "", "", "", "", errors.New("login session expired — try `hookwave login` again")
				}
				// Soft errors during poll: keep going.
				continue
			}
			switch p.Data.Status {
			case "approved":
				return p.Data.AccessToken, p.Data.User.ID, p.Data.User.Email, p.Data.Org.ID, nil
			case "expired":
				return "", "", "", "", errors.New("login session expired — try `hookwave login` again")
			default:
				// "pending" — keep polling.
			}
		}
	}
}

func openBrowser(url string) error {
	// Tiny wrapper; pulling in github.com/pkg/browser would work too,
	// but exec is fine and avoids the dep.
	switch runtime.GOOS {
	case "darwin":
		return runDetached("open", url)
	case "windows":
		return runDetached("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		return runDetached("xdg-open", url)
	}
}
