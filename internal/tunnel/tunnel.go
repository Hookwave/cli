// Package tunnel implements the websocket client + local HTTP
// forwarder for `hookwave listen`. The protocol is intentionally
// minimal:
//
//   server → client: { "type":"event", "event": { ... } }
//   client → server: { "type":"ack",  "id":"<event-id>", "status":<int>, "ms":<int> }
//
// The server keeps unacked events queued for the connected session.
// On reconnect the client gets any unacked events again.
package tunnel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// Event mirrors the server's event payload. We only decode what we
// need to forward to localhost.
type Event struct {
	ID      string            `json:"id"`
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
	// BodyEncoding is "utf8" or "base64". The server picks based on
	// content-type to keep binary bodies intact.
	BodyEncoding string `json:"bodyEncoding"`
	// SourceName is informational, displayed to the user.
	SourceName string `json:"sourceName"`
}

// Inbound message from the server.
type inbound struct {
	Type  string `json:"type"`
	Event *Event `json:"event,omitempty"`
}

// Outbound message to the server.
type outbound struct {
	Type   string `json:"type"`
	ID     string `json:"id,omitempty"`
	Status int    `json:"status,omitempty"`
	Ms     int    `json:"ms,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Options control the tunnel behaviour.
type Options struct {
	// APIBase like "https://api.hookwave.dev".
	APIBase string
	// Bearer token from internal/auth.
	Token string
	// Source IDs to subscribe to. Empty = all sources in active org.
	SourceIDs []string
	// Server-side substring filters. Empty fields = no filter.
	FilterBody    string
	FilterHeaders string
	FilterPath    string
	FilterQuery   string
	// LocalURL like "http://localhost:3000".
	LocalURL string
	// LocalTimeout caps each forwarded request.
	LocalTimeout time.Duration
	// OnEvent is a hook for the TUI / verbose mode. Optional.
	OnEvent func(e *Event, status int, ms int, err error)
	// UserAgent for the websocket upgrade request.
	UserAgent string
}

// Run dials the tunnel and forwards events until ctx is cancelled or
// the connection is closed permanently. It reconnects with backoff
// on transient errors so a flaky network doesn't kill the session.
func Run(ctx context.Context, o Options) error {
	if o.LocalURL == "" {
		return errors.New("LocalURL is required")
	}
	if o.LocalTimeout == 0 {
		o.LocalTimeout = 30 * time.Second
	}
	backoff := newBackoff(time.Second, 30*time.Second)
	for {
		err := runOnce(ctx, o)
		if err == nil || errors.Is(err, context.Canceled) {
			return err
		}
		// Retry with backoff. Permanent errors (auth, 4xx upgrade) are
		// returned by runOnce wrapped in errAuth so we exit fast.
		var ae *errAuth
		if errors.As(err, &ae) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff.next()):
		}
	}
}

func runOnce(ctx context.Context, o Options) error {
	wsURL, err := buildWSURL(o.APIBase, o.SourceIDs, filterMap(o))
	if err != nil {
		return err
	}
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer "+o.Token)
	if o.UserAgent != "" {
		hdr.Set("User-Agent", o.UserAgent)
	}

	conn, resp, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: hdr,
	})
	if err != nil {
		if resp != nil && (resp.StatusCode == 401 || resp.StatusCode == 403) {
			return &errAuth{Status: resp.StatusCode}
		}
		return fmt.Errorf("dial: %w", err)
	}
	// 64MB read limit — large enough for any reasonable webhook body.
	conn.SetReadLimit(64 * 1024 * 1024)
	defer conn.CloseNow()

	hc := &http.Client{Timeout: o.LocalTimeout}
	for {
		var msg inbound
		if err := wsjson.Read(ctx, conn, &msg); err != nil {
			if errors.Is(err, context.Canceled) {
				return err
			}
			return fmt.Errorf("read: %w", err)
		}
		switch msg.Type {
		case "event":
			if msg.Event == nil {
				continue
			}
			start := time.Now()
			status, ferr := forward(ctx, hc, o.LocalURL, msg.Event)
			ms := int(time.Since(start) / time.Millisecond)
			if o.OnEvent != nil {
				o.OnEvent(msg.Event, status, ms, ferr)
			}
			out := outbound{Type: "ack", ID: msg.Event.ID, Status: status, Ms: ms}
			if ferr != nil {
				out.Error = ferr.Error()
			}
			if err := wsjson.Write(ctx, conn, out); err != nil {
				return fmt.Errorf("ack: %w", err)
			}
		case "ping":
			_ = wsjson.Write(ctx, conn, outbound{Type: "pong"})
		default:
			// Forward-compatible: ignore unknown frame types.
		}
	}
}

func forward(ctx context.Context, hc *http.Client, localURL string, e *Event) (int, error) {
	target, err := buildLocalTarget(localURL, e.Path)
	if err != nil {
		return 0, err
	}
	body, err := decodeBody(e)
	if err != nil {
		return 0, err
	}
	method := strings.ToUpper(strings.TrimSpace(e.Method))
	if method == "" {
		method = http.MethodPost
	}
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	for k, v := range e.Headers {
		// Skip hop-by-hop headers — they don't survive the proxy.
		if isHopByHop(k) {
			continue
		}
		req.Header.Set(k, v)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}

func buildLocalTarget(base, eventPath string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	if eventPath != "" {
		u.Path = strings.TrimRight(u.Path, "/") + "/" + strings.TrimLeft(eventPath, "/")
	}
	return u.String(), nil
}

func decodeBody(e *Event) ([]byte, error) {
	switch e.BodyEncoding {
	case "", "utf8":
		return []byte(e.Body), nil
	case "base64":
		return base64Decode(e.Body)
	}
	return nil, fmt.Errorf("unknown body encoding %q", e.BodyEncoding)
}

func buildWSURL(apiBase string, sources []string, filters map[string]string) (string, error) {
	u, err := url.Parse(apiBase)
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	default:
		return "", fmt.Errorf("unsupported scheme %q", u.Scheme)
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/v1/cli/listen"
	q := u.Query()
	if len(sources) > 0 {
		q.Set("sources", strings.Join(sources, ","))
	}
	for k, v := range filters {
		if v != "" {
			q.Set(k, v)
		}
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func filterMap(o Options) map[string]string {
	return map[string]string{
		"filterBody":    o.FilterBody,
		"filterHeaders": o.FilterHeaders,
		"filterPath":    o.FilterPath,
		"filterQuery":   o.FilterQuery,
	}
}

// errAuth is returned for HTTP 401/403 on the websocket upgrade so
// Run can exit immediately without retrying.
type errAuth struct{ Status int }

func (e *errAuth) Error() string {
	return fmt.Sprintf("authentication failed (HTTP %d) — try `hookwave login` again", e.Status)
}

// JSONFor returns a json-marshalled version of the inbound event;
// used by the TUI to display payloads.
func JSONFor(e *Event) string {
	b, _ := json.MarshalIndent(e, "", "  ")
	return string(b)
}
