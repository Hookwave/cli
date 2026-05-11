// Package auth handles secure storage of CLI access tokens. Tokens are
// kept in the OS keychain (macOS Keychain / GNOME libsecret /
// Windows Credential Manager) so they never touch the filesystem in
// plaintext. Falls back to a 0600 file under the config dir if the
// keyring is unavailable (CI runners, headless servers).
package auth

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/zalando/go-keyring"
)

const (
	keyringService = "hookwave-cli"
	keyringUser    = "default"
)

var (
	// ErrNoToken is returned when no token has been stored. Callers can
	// distinguish "logged out" from real errors.
	ErrNoToken = errors.New("no token stored")

	mu sync.Mutex
)

// SetToken writes the token to the keyring (preferred) or falls back
// to a 0600 file. Always call this with a non-empty token.
func SetToken(token string) error {
	mu.Lock()
	defer mu.Unlock()
	if strings.TrimSpace(token) == "" {
		return errors.New("token is empty")
	}
	if err := keyring.Set(keyringService, keyringUser, token); err == nil {
		// Clean up any stale fallback file so we don't end up with two
		// sources of truth.
		_ = os.Remove(fallbackPath())
		return nil
	}
	// Keyring unavailable — write to fallback file.
	return writeFallback(token)
}

// GetToken returns the stored token, ErrNoToken when none, or another
// error if the keyring fails AND no fallback is present.
func GetToken() (string, error) {
	mu.Lock()
	defer mu.Unlock()
	if v, err := keyring.Get(keyringService, keyringUser); err == nil {
		return v, nil
	} else if !errors.Is(err, keyring.ErrNotFound) {
		// Try fallback if the keyring itself is broken.
		if v, ferr := readFallback(); ferr == nil {
			return v, nil
		}
		return "", fmt.Errorf("keyring lookup: %w", err)
	}
	v, err := readFallback()
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", ErrNoToken
		}
		return "", err
	}
	return v, nil
}

// DeleteToken removes the token from both the keyring and the fallback
// file (best-effort on each).
func DeleteToken() error {
	mu.Lock()
	defer mu.Unlock()
	_ = keyring.Delete(keyringService, keyringUser)
	if err := os.Remove(fallbackPath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove fallback: %w", err)
	}
	return nil
}

func fallbackDir() string {
	if d := os.Getenv("HOOKWAVE_CONFIG_DIR"); d != "" {
		return d
	}
	xdg := os.Getenv("XDG_CONFIG_HOME")
	if xdg == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		xdg = filepath.Join(home, ".config")
	}
	return filepath.Join(xdg, "hookwave")
}

func fallbackPath() string { return filepath.Join(fallbackDir(), "token") }

func writeFallback(token string) error {
	d := fallbackDir()
	if d == "" {
		return errors.New("no config dir available for fallback")
	}
	if err := os.MkdirAll(d, 0o700); err != nil {
		return fmt.Errorf("create fallback dir: %w", err)
	}
	tmp := fallbackPath() + ".tmp"
	if err := os.WriteFile(tmp, []byte(token), 0o600); err != nil {
		return fmt.Errorf("write fallback: %w", err)
	}
	return os.Rename(tmp, fallbackPath())
}

func readFallback() (string, error) {
	b, err := os.ReadFile(fallbackPath())
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}
