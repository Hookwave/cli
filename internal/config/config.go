// Package config persists CLI state (active org, API base URL) under
// the XDG Base Directory spec. Secrets (auth tokens) are stored
// separately in the OS keyring — see internal/auth.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// DefaultAPIBase is the production API URL. Override via $HOOKWAVE_API.
const DefaultAPIBase = "https://api.hookwave.com"

// Config is the shape persisted to disk. New fields must be optional
// to preserve backward compatibility with older configs.
type Config struct {
	APIBase    string `json:"apiBase,omitempty"`
	ActiveOrg  string `json:"activeOrg,omitempty"`
	UserID     string `json:"userId,omitempty"`
	UserEmail  string `json:"userEmail,omitempty"`
	// Schema is bumped if we ever need to migrate the on-disk format.
	Schema int `json:"schema,omitempty"`
}

const currentSchema = 1

// File path resolution. We follow XDG Base Directory:
//   $XDG_CONFIG_HOME/hookwave/config.json   (default ~/.config/hookwave/...)
// Override the directory entirely via $HOOKWAVE_CONFIG_DIR for tests.
func dir() (string, error) {
	if d := os.Getenv("HOOKWAVE_CONFIG_DIR"); d != "" {
		return d, nil
	}
	xdg := os.Getenv("XDG_CONFIG_HOME")
	if xdg == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home dir: %w", err)
		}
		xdg = filepath.Join(home, ".config")
	}
	return filepath.Join(xdg, "hookwave"), nil
}

func filePath() (string, error) {
	d, err := dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "config.json"), nil
}

var loadMu sync.Mutex

// Load reads the config file. Returns a zero-value config if the file
// doesn't exist (first run).
func Load() (*Config, error) {
	loadMu.Lock()
	defer loadMu.Unlock()

	path, err := filePath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &Config{Schema: currentSchema}, nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if c.Schema == 0 {
		c.Schema = currentSchema
	}
	return &c, nil
}

// Save atomically writes the config back to disk with 0600 perms.
// Atomic = write to a sibling .tmp file then rename.
func Save(c *Config) error {
	loadMu.Lock()
	defer loadMu.Unlock()

	d, err := dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(d, 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	c.Schema = currentSchema
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	path := filepath.Join(d, "config.json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("write temp config: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename config: %w", err)
	}
	return nil
}

// APIBase returns the resolved base URL: env override > config > default.
func (c *Config) ResolvedAPIBase() string {
	if v := os.Getenv("HOOKWAVE_API"); v != "" {
		return v
	}
	if c.APIBase != "" {
		return c.APIBase
	}
	return DefaultAPIBase
}
