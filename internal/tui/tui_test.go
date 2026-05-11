package tui

import (
	"testing"

	"github.com/hookwave/hookwave/apps/cli/internal/tunnel"
)

func TestMatchesFilter(t *testing.T) {
	e := entry{event: &tunnel.Event{
		Method:     "POST",
		Path:       "/v1/charges/created",
		SourceName: "stripe-prod",
	}}
	cases := []struct {
		q    string
		want bool
	}{
		{"", true},
		{"post", true},
		{"POST", true}, // filter is lowercased before this point
		{"charges", true},
		{"stripe", true},
		{"stripe-prod", true},
		{"github", false},
		{"/v2/", false},
	}
	for _, tc := range cases {
		// In production filter is lowercased on apply; mirror that here.
		got := matchesFilter(e, lower(tc.q))
		if got != tc.want {
			t.Errorf("matchesFilter(%q) = %v, want %v", tc.q, got, tc.want)
		}
	}
}

func TestVisibleIndicesUnfiltered(t *testing.T) {
	m := &model{
		entries: []entry{
			{event: &tunnel.Event{Path: "/a"}},
			{event: &tunnel.Event{Path: "/b"}},
			{event: &tunnel.Event{Path: "/c"}},
		},
	}
	got := m.visibleIndices()
	if len(got) != 3 || got[0] != 0 || got[1] != 1 || got[2] != 2 {
		t.Fatalf("unfiltered visibleIndices = %v", got)
	}
}

func TestVisibleIndicesFiltered(t *testing.T) {
	m := &model{
		entries: []entry{
			{event: &tunnel.Event{Path: "/charges/abc"}},
			{event: &tunnel.Event{Path: "/refunds/xyz"}},
			{event: &tunnel.Event{Path: "/charges/def"}},
		},
		filter: "charges",
	}
	got := m.visibleIndices()
	if len(got) != 2 || got[0] != 0 || got[1] != 2 {
		t.Fatalf("filtered visibleIndices = %v", got)
	}
}

func lower(s string) string {
	out := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		out[i] = c
	}
	return string(out)
}
