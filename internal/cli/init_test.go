package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectStack(t *testing.T) {
	dir := t.TempDir()
	if got := detectStack(dir); got != "none" {
		t.Errorf("empty dir → got %q, want none", got)
	}
	must(t, os.WriteFile(filepath.Join(dir, "package.json"), []byte("{}"), 0o644))
	if got := detectStack(dir); got != "javascript" {
		t.Errorf("package.json only → got %q, want javascript", got)
	}
	must(t, os.WriteFile(filepath.Join(dir, "tsconfig.json"), []byte("{}"), 0o644))
	if got := detectStack(dir); got != "typescript" {
		t.Errorf("package.json + tsconfig → got %q, want typescript", got)
	}

	dir2 := t.TempDir()
	must(t, os.WriteFile(filepath.Join(dir2, "go.mod"), []byte("module x"), 0o644))
	if got := detectStack(dir2); got != "go" {
		t.Errorf("go.mod → got %q, want go", got)
	}

	dir3 := t.TempDir()
	must(t, os.WriteFile(filepath.Join(dir3, "pyproject.toml"), []byte(""), 0o644))
	if got := detectStack(dir3); got != "python" {
		t.Errorf("pyproject.toml → got %q, want python", got)
	}
}

func TestStubFor(t *testing.T) {
	cases := []struct {
		lang string
		ext  string
		want string // a substring guaranteed to be in the output
	}{
		{"typescript", "ts", "handleStripeWebhook"},
		{"javascript", "js", "handleStripeWebhook"},
		{"python", "py", "handle_stripe_webhook"},
		{"go", "go", "HandleStripeWebhook"},
	}
	for _, c := range cases {
		ext, body, err := stubFor("stripe", c.lang)
		if err != nil {
			t.Fatalf("%s: %v", c.lang, err)
		}
		if ext != c.ext {
			t.Errorf("%s: ext = %q, want %q", c.lang, ext, c.ext)
		}
		if !strings.Contains(body, c.want) {
			t.Errorf("%s: body missing %q", c.lang, c.want)
		}
	}
}

func TestWriteHandlerStubLeavesExisting(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "stripe.ts")
	must(t, os.WriteFile(target, []byte("// custom code"), 0o644))

	path, err := writeHandlerStub(dir, "stripe", "typescript", false)
	if err != nil {
		t.Fatalf("writeHandlerStub: %v", err)
	}
	if path != "" {
		t.Errorf("expected no overwrite, got path=%q", path)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "// custom code" {
		t.Errorf("file overwritten unexpectedly: %q", got)
	}
}

func TestWriteHandlerStubForce(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "stripe.ts")
	must(t, os.WriteFile(target, []byte("// old"), 0o644))

	path, err := writeHandlerStub(dir, "stripe", "typescript", true)
	if err != nil {
		t.Fatalf("force: %v", err)
	}
	if path != target {
		t.Errorf("path = %q, want %q", path, target)
	}
	got, _ := os.ReadFile(target)
	if !strings.Contains(string(got), "handleStripeWebhook") {
		t.Errorf("force did not overwrite: %q", got)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
}
