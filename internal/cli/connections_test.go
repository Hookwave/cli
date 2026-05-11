package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadBodyFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.json")
	if err := os.WriteFile(p, []byte(`{"a":1}`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := readBodyFile(p)
	if err != nil {
		t.Fatalf("readBodyFile: %v", err)
	}
	if string(got) != `{"a":1}` {
		t.Fatalf("got %q", string(got))
	}
}
