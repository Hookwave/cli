package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveAndLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOOKWAVE_CONFIG_DIR", dir)

	c, err := Load()
	if err != nil {
		t.Fatalf("Load empty: %v", err)
	}
	if c.Schema != currentSchema {
		t.Fatalf("Schema = %d, want %d", c.Schema, currentSchema)
	}

	c.ActiveOrg = "org_123"
	c.UserEmail = "ruben@example.com"
	if err := Save(c); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.ActiveOrg != "org_123" || got.UserEmail != "ruben@example.com" {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}

	// File must be 0600.
	info, err := os.Stat(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("perm = %o, want 0600", info.Mode().Perm())
	}
}

func TestResolvedAPIBase(t *testing.T) {
	// Ensure the developer's shell env doesn't leak in and shadow the
	// default. Setenv with "" temporarily clears it for this test.
	t.Setenv("HOOKWAVE_API", "")
	c := &Config{}
	if got := c.ResolvedAPIBase(); got != DefaultAPIBase {
		t.Fatalf("default: got %q want %q", got, DefaultAPIBase)
	}
	c.APIBase = "https://api.example.com"
	if got := c.ResolvedAPIBase(); got != "https://api.example.com" {
		t.Fatalf("config base: got %q", got)
	}
	t.Setenv("HOOKWAVE_API", "http://override")
	if got := c.ResolvedAPIBase(); got != "http://override" {
		t.Fatalf("env override: got %q", got)
	}
}
