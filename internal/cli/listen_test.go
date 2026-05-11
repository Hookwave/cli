package cli

import (
	"testing"
)

func TestNormalizeLocalTarget(t *testing.T) {
	cases := []struct {
		name string
		arg  string
		host string
		want string
	}{
		{"plain port", "3000", "127.0.0.1", "http://127.0.0.1:3000"},
		{"plain port localhost", "8080", "localhost", "http://localhost:8080"},
		{"host:port", "0.0.0.0:9000", "", "http://0.0.0.0:9000"},
		{"http url", "http://localhost:3000", "", "http://localhost:3000"},
		{"https url with trailing slash", "https://example.com/", "", "https://example.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeLocalTarget(tc.arg, tc.host)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestNormalizeLocalTargetRejectsGarbage(t *testing.T) {
	if _, err := normalizeLocalTarget("not a port or url", ""); err == nil {
		t.Fatal("expected error for garbage input")
	}
	if _, err := normalizeLocalTarget("", ""); err == nil {
		t.Fatal("expected error for empty input")
	}
}

func TestDashboardURLFromAPI(t *testing.T) {
	cases := []struct {
		api  string
		want string
	}{
		{"https://api.hookwave.com", "https://app.hookwave.com"},
		{"https://api.hookwave.com/", "https://app.hookwave.com"},
		{"http://localhost:3002", "http://localhost:5173"},
		{"http://127.0.0.1:3002", "http://localhost:5173"},
		{"https://hookwave.example.com", ""}, // no api. prefix → no guess
	}
	for _, tc := range cases {
		if got := dashboardURLFromAPI(tc.api); got != tc.want {
			t.Errorf("dashboardURLFromAPI(%q) = %q, want %q", tc.api, got, tc.want)
		}
	}
}
