// Package httpc is the authenticated HTTP client used by every
// CLI subcommand to talk to the Hookwave API. Centralised so we
// only configure timeouts, redirect handling, and auth in one place.
package httpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client wraps http.Client with the API base URL and bearer token.
type Client struct {
	base   string
	token  string
	hc     *http.Client
	uaName string
}

// New returns a Client. Pass an empty token for unauthenticated
// endpoints (e.g. /v1/cli/auth/device).
func New(baseURL, bearer, userAgent string) *Client {
	return &Client{
		base:   strings.TrimRight(baseURL, "/"),
		token:  bearer,
		uaName: userAgent,
		hc: &http.Client{
			Timeout: 30 * time.Second,
			// Block redirects on API calls — surfaces auth issues
			// instead of silently following 30x.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// APIError is returned when the server responds with a non-2xx
// status. The Code/Message map to Hookwave's error envelope.
type APIError struct {
	Status  int    `json:"-"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

func (e *APIError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("api %d %s: %s", e.Status, e.Code, e.Message)
	}
	if e.Message != "" {
		return fmt.Sprintf("api %d: %s", e.Status, e.Message)
	}
	return fmt.Sprintf("api %d", e.Status)
}

// Do issues the given request and JSON-decodes the response into out
// (pass nil to discard). Returns *APIError for non-2xx.
func (c *Client) Do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("encode body: %w", err)
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if c.uaName != "" {
		req.Header.Set("User-Agent", c.uaName)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		// Best-effort decode of the error envelope. If we can't, surface
		// the raw body up to a sane cap to avoid log floods.
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
		ae := &APIError{Status: resp.StatusCode, Message: strings.TrimSpace(string(raw))}
		if json.Valid(raw) {
			_ = json.Unmarshal(raw, ae)
			ae.Status = resp.StatusCode
		}
		return ae
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		if errors.Is(err, io.EOF) {
			return nil // no body, that's fine
		}
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// Get is a thin convenience wrapper around Do.
func (c *Client) Get(ctx context.Context, path string, out any) error {
	return c.Do(ctx, http.MethodGet, path, nil, out)
}

// Post is a thin convenience wrapper around Do.
func (c *Client) Post(ctx context.Context, path string, in, out any) error {
	return c.Do(ctx, http.MethodPost, path, in, out)
}

// Delete is a thin convenience wrapper around Do.
func (c *Client) Delete(ctx context.Context, path string) error {
	return c.Do(ctx, http.MethodDelete, path, nil, nil)
}

// Base returns the configured base URL (handy for constructing
// websocket URLs from the same source of truth).
func (c *Client) Base() string { return c.base }

// Token returns the configured bearer token (used by tunnel code that
// builds its own websocket request).
func (c *Client) Token() string { return c.token }
