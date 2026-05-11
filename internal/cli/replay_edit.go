package cli

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hookwave/hookwave/apps/cli/internal/httpc"
	"github.com/hookwave/hookwave/apps/cli/internal/output"
)

type bodyDownloadResp struct {
	Data struct {
		URL        string `json:"url"`
		SHA256     string `json:"sha256"`
		SizeBytes  int    `json:"size_bytes"`
		TTLSeconds int    `json:"ttl_seconds"`
	} `json:"data"`
}

// runReplayWithEdit downloads the body, opens $EDITOR, then POSTs to
// the replay-with-body endpoint with the user-modified contents.
func runReplayWithEdit(ctx context.Context, a *app, c *httpc.Client, eventID string) error {
	a.stdout.Println(output.Muted, "Fetching current body…")

	var dl bodyDownloadResp
	if err := c.Get(ctx, "/v1/events/"+eventID+"/body", &dl); err != nil {
		return fmt.Errorf("fetch body url: %w", err)
	}

	bytes, err := downloadSignedURL(ctx, dl.Data.URL)
	if err != nil {
		return fmt.Errorf("download body: %w", err)
	}

	contentType := "application/json"
	editable := isLikelyText(bytes)
	encoding := "utf8"
	var initial string
	if editable {
		initial = string(bytes)
	} else {
		initial = base64.StdEncoding.EncodeToString(bytes)
		encoding = "base64"
		contentType = "application/octet-stream"
		a.stdout.Println(output.Warn, "Body looks binary — opening as base64. Edit carefully.")
	}

	edited, err := openInEditor(initial, editable)
	if err != nil {
		return err
	}
	if edited == initial {
		a.stdout.Println(output.Muted, "No changes — aborting replay.")
		return nil
	}

	a.stdout.Println(output.Muted, "Replaying with modified body…")
	body := map[string]any{
		"body":        edited,
		"encoding":    encoding,
		"contentType": contentType,
	}
	if err := c.Post(ctx, "/v1/events/"+eventID+"/replay-with-body", body, nil); err != nil {
		return err
	}
	a.stdout.Printf(output.Success, "✓ Replayed %s with edited body\n", eventID)
	return nil
}

func downloadSignedURL(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	hc := &http.Client{Timeout: 30 * time.Second}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("storage GET failed: %s", resp.Status)
	}
	// 16MB ceiling — way past any reasonable webhook body. Defensive only.
	return io.ReadAll(io.LimitReader(resp.Body, 16*1024*1024))
}

// isLikelyText returns true if the bytes are valid UTF-8 with no NULs
// or other ASCII-control bytes typical of binary payloads.
func isLikelyText(b []byte) bool {
	if !utf8.Valid(b) {
		return false
	}
	for _, c := range b {
		if c == 0 {
			return false
		}
	}
	return true
}

// openInEditor writes the initial value to a temp file, runs $EDITOR
// (or vi as fallback), reads back the modified contents.
func openInEditor(initial string, asText bool) (string, error) {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = os.Getenv("VISUAL")
	}
	if editor == "" {
		editor = "vi"
	}

	suffix := ".json"
	if !asText {
		suffix = ".b64"
	}
	tmp, err := os.CreateTemp("", "hookwave-replay-*"+suffix)
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := tmp.WriteString(initial); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}

	cmd := exec.Command(splitEditor(editor)[0], append(splitEditor(editor)[1:], tmpPath)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("editor exited with error: %w", err)
	}

	out, err := os.ReadFile(tmpPath)
	if err != nil {
		return "", err
	}
	// Trim a single trailing newline (editors love adding them).
	s := string(out)
	s = strings.TrimRight(s, "\n")
	return s, nil
}

// splitEditor handles "code --wait" style commands so we don't pass
// the args to exec.Command literally.
func splitEditor(editor string) []string {
	parts := strings.Fields(editor)
	if len(parts) == 0 {
		return []string{"vi"}
	}
	return parts
}

// suppress unused-import warning for filepath until we need it.
var _ = filepath.Join
