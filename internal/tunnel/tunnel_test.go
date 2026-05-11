package tunnel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestBuildWSURL(t *testing.T) {
	cases := []struct {
		base    string
		sources []string
		want    string
	}{
		{"https://api.hookwave.com", nil, "wss://api.hookwave.com/v1/cli/listen"},
		{"http://localhost:3002/", nil, "ws://localhost:3002/v1/cli/listen"},
		{"https://api.hookwave.com", []string{"src_a", "src_b"}, "wss://api.hookwave.com/v1/cli/listen?sources=src_a%2Csrc_b"},
	}
	for _, tc := range cases {
		got, err := buildWSURL(tc.base, tc.sources, nil)
		if err != nil {
			t.Fatalf("buildWSURL: %v", err)
		}
		if got != tc.want {
			t.Fatalf("got %q want %q", got, tc.want)
		}
	}
}

func TestForwardCallsLocalServerWithBodyAndHeaders(t *testing.T) {
	var hits atomic.Int32
	var gotBody string
	var gotPath string
	var gotMethod string
	var gotHeader string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotHeader = r.Header.Get("X-Custom")
		buf := make([]byte, 1024)
		n, _ := r.Body.Read(buf)
		gotBody = string(buf[:n])
		w.WriteHeader(202)
	}))
	defer srv.Close()

	hc := &http.Client{Timeout: 5 * time.Second}
	e := &Event{
		ID:           "evt_1",
		Method:       "POST",
		Path:         "/hook/path",
		Headers:      map[string]string{"X-Custom": "yes", "Connection": "must-be-stripped"},
		Body:         `{"hello":"world"}`,
		BodyEncoding: "utf8",
	}
	status, err := forward(context.Background(), hc, srv.URL, e)
	if err != nil {
		t.Fatalf("forward: %v", err)
	}
	if status != 202 {
		t.Fatalf("status = %d, want 202", status)
	}
	if hits.Load() != 1 {
		t.Fatalf("hits = %d, want 1", hits.Load())
	}
	if gotMethod != "POST" {
		t.Fatalf("method = %q", gotMethod)
	}
	if !strings.HasSuffix(gotPath, "/hook/path") {
		t.Fatalf("path = %q", gotPath)
	}
	if gotHeader != "yes" {
		t.Fatalf("X-Custom = %q", gotHeader)
	}
	if gotBody != `{"hello":"world"}` {
		t.Fatalf("body = %q", gotBody)
	}
}

func TestForwardBase64Body(t *testing.T) {
	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 16)
		n, _ := r.Body.Read(buf)
		captured = buf[:n]
		w.WriteHeader(200)
	}))
	defer srv.Close()

	// "PNG\r\n" — picked because it's not valid UTF-8 in some contexts.
	want := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a}
	encoded := "iVBORw0K"
	e := &Event{
		ID:           "evt_2",
		Method:       "POST",
		Path:         "/",
		Headers:      map[string]string{},
		Body:         encoded,
		BodyEncoding: "base64",
	}
	if _, err := forward(context.Background(), &http.Client{Timeout: 5 * time.Second}, srv.URL, e); err != nil {
		t.Fatalf("forward: %v", err)
	}
	if string(captured) != string(want) {
		t.Fatalf("captured bytes %v want %v", captured, want)
	}
}
