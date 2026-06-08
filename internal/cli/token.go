package cli

import "github.com/hookwave/cli/internal/auth"

// tokenFromAuth is a thin pass-through used by listen so its file
// stays small. Errors are surfaced; callers swallow ErrNoToken because
// the authed-client check upstream already gates on that.
func tokenFromAuth() (string, error) {
	t, err := auth.GetToken()
	if err != nil {
		return "", err
	}
	return t, nil
}
