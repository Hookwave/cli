package cli

import (
	"strings"
	"testing"
	"time"
)

type ev = struct {
	ID         string    `json:"id"`
	Status     string    `json:"status"`
	Verified   bool      `json:"verified"`
	ReceivedAt time.Time `json:"receivedAt"`
	Deliveries []delivery `json:"deliveries"`
}

func TestAnalyseEvent_VerificationFail(t *testing.T) {
	e := &ev{Status: "dropped", Verified: false}
	c := analyseEvent(e)
	if len(c) != 1 || !strings.Contains(c[0].Title, "verification") {
		t.Fatalf("expected verification-fail cause, got %+v", c)
	}
}

func TestAnalyseEvent_401SigningHint(t *testing.T) {
	e := &ev{
		Verified: true,
		Status:   "failed",
		Deliveries: []delivery{
			{ResponseStatus: 401, ResponseBodySnippet: "Invalid signature"},
		},
	}
	c := analyseEvent(e)
	if len(c) != 1 || !strings.Contains(strings.ToLower(c[0].Title), "rejected") {
		t.Fatalf("expected 401-rejected cause, got %+v", c)
	}
	if !strings.Contains(strings.ToLower(c[0].Suggestion), "signing") {
		t.Fatalf("suggestion should mention signing: %q", c[0].Suggestion)
	}
}

func TestAnalyseEvent_TimeoutPattern(t *testing.T) {
	e := &ev{
		Verified: true,
		Status:   "failed",
		Deliveries: []delivery{
			{ResponseStatus: 0, ErrorClass: "timeout", ErrorMessage: "i/o timeout", DurationMs: 30000},
		},
	}
	c := analyseEvent(e)
	if len(c) != 1 || !strings.Contains(strings.ToLower(c[0].Title), "timed out") {
		t.Fatalf("expected timeout cause, got %+v", c)
	}
}

func TestAnalyseEvent_DNSFailure(t *testing.T) {
	e := &ev{
		Verified: true,
		Status:   "failed",
		Deliveries: []delivery{
			{ErrorClass: "dial", ErrorMessage: "lookup api.broken.example: no such host"},
		},
	}
	c := analyseEvent(e)
	if len(c) != 1 || !strings.Contains(c[0].Title, "DNS") {
		t.Fatalf("expected DNS cause, got %+v", c)
	}
}

func TestAnalyseEvent_ConsistentFiveHundreds(t *testing.T) {
	e := &ev{
		Verified: true,
		Status:   "failed",
		Deliveries: []delivery{
			{ResponseStatus: 502}, {ResponseStatus: 503}, {ResponseStatus: 502},
		},
	}
	c := analyseEvent(e)
	if len(c) != 1 || !strings.Contains(strings.ToLower(c[0].Title), "consistently") {
		t.Fatalf("expected 5xx-consistent cause, got %+v", c)
	}
}

func TestAnalyseEvent_NoMatchingPattern(t *testing.T) {
	e := &ev{
		Verified: true,
		Status:   "delivered",
		Deliveries: []delivery{
			{ResponseStatus: 200},
		},
	}
	c := analyseEvent(e)
	if len(c) != 0 {
		t.Fatalf("happy path should yield no causes, got %+v", c)
	}
}
