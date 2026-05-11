package cli

import (
	"os"
	"path/filepath"
)

// detectStack returns a likely default for the handler-language prompt
// based on package files in dir. Returns "none" when no signal.
func detectStack(dir string) string {
	if exists(filepath.Join(dir, "package.json")) {
		if exists(filepath.Join(dir, "tsconfig.json")) {
			return "typescript"
		}
		return "javascript"
	}
	if exists(filepath.Join(dir, "go.mod")) {
		return "go"
	}
	if exists(filepath.Join(dir, "pyproject.toml")) || exists(filepath.Join(dir, "requirements.txt")) {
		return "python"
	}
	return "none"
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
