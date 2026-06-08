package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/hookwave/cli/internal/output"
)

// `hookwave doctor` — explains why a delivery failed.
//
// Hookdeck's CLI shows you the status code; you're on your own from
// there. This command pattern-matches on the response + symptoms and
// emits ranked likely causes with a suggested next step. Catalog is
// intentionally narrow: only patterns we have high confidence in.
// Better to say "I don't know" than guess wrong.

type eventDetailDoctor struct {
	Data struct {
		ID         string    `json:"id"`
		Status     string    `json:"status"`
		Verified   bool      `json:"verified"`
		ReceivedAt time.Time `json:"receivedAt"`
		Deliveries []delivery `json:"deliveries"`
	} `json:"data"`
}

type delivery struct {
	ID                 string    `json:"id"`
	ConnectionID       string    `json:"connectionId"`
	StartedAt          time.Time `json:"startedAt"`
	CompletedAt        *time.Time `json:"completedAt"`
	ResponseStatus     int       `json:"responseStatus"`
	ResponseBodySnippet string    `json:"responseBodySnippet"`
	ErrorClass         string    `json:"errorClass"`
	ErrorMessage       string    `json:"errorMessage"`
	DurationMs         int       `json:"durationMs"`
}

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor <event-id>",
		Short: "Explain why an event failed (or is failing)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a := appFrom(cmd)
			c, err := a.authedClient()
			if err != nil {
				return err
			}
			var r eventDetailDoctor
			if err := c.Get(cmd.Context(), "/v1/events/"+args[0], &r); err != nil {
				return err
			}

			a.stdout.Printf(output.None, "\n  Event %s\n", a.stdout.Stylize(output.None, r.Data.ID))
			a.stdout.Printf(output.Muted, "  status: %s • attempts: %d\n\n", r.Data.Status, len(r.Data.Deliveries))

			causes := analyseEvent(&r.Data)
			if len(causes) == 0 {
				a.stdout.Println(output.Muted, "  no recognized failure pattern. Likely intermittent or a new failure mode.")
				return nil
			}
			for i, c := range causes {
				bullet := a.stdout.Stylize(output.Warn, "•")
				a.stdout.Printf(output.None, "  %s %s\n", bullet, a.stdout.Stylize(output.None, c.Title))
				if c.Evidence != "" {
					a.stdout.Printf(output.Muted, "    evidence:    %s\n", c.Evidence)
				}
				a.stdout.Printf(output.Muted, "    suggestion:  %s\n", c.Suggestion)
				if i < len(causes)-1 {
					a.stdout.Println(output.None, "")
				}
			}
			a.stdout.Println(output.None, "")
			return nil
		},
	}
}

type cause struct {
	Title      string
	Evidence   string
	Suggestion string
}

// analyseEvent runs all pattern matchers and returns ranked causes
// (most likely first). Each matcher is independent and conservative —
// emit only when the symptom is strong.
func analyseEvent(e *struct {
	ID         string    `json:"id"`
	Status     string    `json:"status"`
	Verified   bool      `json:"verified"`
	ReceivedAt time.Time `json:"receivedAt"`
	Deliveries []delivery `json:"deliveries"`
}) []cause {
	var out []cause

	// Verification side — these are independent of delivery attempts.
	if !e.Verified && e.Status == "dropped" {
		out = append(out, cause{
			Title:      "Inbound verification failed → event dropped",
			Evidence:   "verified=false and status=dropped",
			Suggestion: "Check the source's signing-secret config; the provider HMAC didn't match.",
		})
		return out
	}

	if len(e.Deliveries) == 0 {
		// Failed without ever attempting (e.g. plan gate or fanout had no connections).
		if e.Status == "failed" {
			out = append(out, cause{
				Title:      "Failed before any delivery attempt",
				Suggestion: "No connections matched this source/event, or the org is over its plan gate. Check Connections page filters.",
			})
		}
		return out
	}

	// Per-delivery patterns. We look at the *last* attempt for the
	// "what's going wrong now" picture.
	last := e.Deliveries[len(e.Deliveries)-1]
	allFailed := true
	for _, d := range e.Deliveries {
		if d.ResponseStatus >= 200 && d.ResponseStatus < 300 {
			allFailed = false
			break
		}
	}

	switch {
	case last.ResponseStatus == 401, last.ResponseStatus == 403:
		out = append(out, cause{
			Title:      fmt.Sprintf("Destination rejected the request (%d)", last.ResponseStatus),
			Evidence:   bodySnippet(last),
			Suggestion: "Outbound HMAC secret on the destination probably doesn't match what the receiver expects. Re-sync signing secrets.",
		})
	case last.ResponseStatus == 404:
		out = append(out, cause{
			Title:      "Destination returned 404",
			Evidence:   bodySnippet(last),
			Suggestion: "URL path may be wrong, or the receiver doesn't have a route for this method. Confirm the destination URL.",
		})
	case last.ResponseStatus == 408,
		strings.EqualFold(last.ErrorClass, "timeout"),
		strings.Contains(strings.ToLower(last.ErrorMessage), "timeout"):
		out = append(out, cause{
			Title:      "Receiver timed out",
			Evidence:   fmt.Sprintf("duration_ms=%d, error=%s", last.DurationMs, last.ErrorMessage),
			Suggestion: "Receiver's handler is slower than the connection's request timeout. Either ack-then-process async, or raise the timeout on the connection.",
		})
	case last.ResponseStatus == 413:
		out = append(out, cause{
			Title:      "Payload too large for the receiver",
			Evidence:   bodySnippet(last),
			Suggestion: "Receiver capped body size. Either raise the receiver's limit, or use a transformation to slim the payload.",
		})
	case last.ResponseStatus == 429:
		out = append(out, cause{
			Title:      "Receiver rate-limited us",
			Evidence:   bodySnippet(last),
			Suggestion: "Throttle outbound on this connection (per-connection rate limit) or shed via filters. Hookwave will keep retrying with backoff.",
		})
	case strings.Contains(strings.ToLower(last.ErrorClass), "dns"),
		strings.Contains(strings.ToLower(last.ErrorMessage), "no such host"),
		strings.Contains(strings.ToLower(last.ErrorMessage), "lookup"):
		out = append(out, cause{
			Title:      "DNS resolution failed",
			Evidence:   last.ErrorMessage,
			Suggestion: "Destination hostname is unreachable. Check the URL on the connection — typo or stale domain?",
		})
	case strings.Contains(strings.ToLower(last.ErrorClass), "tls"),
		strings.Contains(strings.ToLower(last.ErrorMessage), "tls"),
		strings.Contains(strings.ToLower(last.ErrorMessage), "certificate"):
		out = append(out, cause{
			Title:      "TLS handshake failed",
			Evidence:   last.ErrorMessage,
			Suggestion: "Receiver's certificate is invalid/expired or the chain is broken. If you control the receiver, fix the cert; if not, contact them.",
		})
	case strings.Contains(strings.ToLower(last.ErrorClass), "connection"),
		strings.Contains(strings.ToLower(last.ErrorMessage), "refused"),
		strings.Contains(strings.ToLower(last.ErrorMessage), "reset by peer"):
		out = append(out, cause{
			Title:      "Connection refused / reset",
			Evidence:   last.ErrorMessage,
			Suggestion: "Receiver isn't accepting connections on this URL — wrong port, firewall, or service is down.",
		})
	case last.ResponseStatus >= 500 && allFailed && len(e.Deliveries) >= 3:
		out = append(out, cause{
			Title:      fmt.Sprintf("Receiver consistently failing with %d", last.ResponseStatus),
			Evidence:   bodySnippet(last),
			Suggestion: "Receiver-side bug. Check their logs; we'll keep retrying with exponential backoff up to the connection's max attempts.",
		})
	}

	// Long-tail signal: every attempt was over the timeout window.
	slowAttempts := 0
	for _, d := range e.Deliveries {
		if d.DurationMs > 25_000 {
			slowAttempts++
		}
	}
	if slowAttempts >= 2 && len(out) == 0 {
		out = append(out, cause{
			Title:      "Multiple slow attempts (>25s)",
			Suggestion: "Receiver is consistently slow. Profile the handler or move work to a background queue.",
		})
	}

	return out
}

func bodySnippet(d delivery) string {
	s := strings.TrimSpace(d.ResponseBodySnippet)
	if s == "" {
		return ""
	}
	if len(s) > 160 {
		s = s[:157] + "…"
	}
	return s
}
