package tunnel

import (
	"encoding/base64"
	"strings"
	"time"
)

func base64Decode(s string) ([]byte, error) {
	s = strings.ReplaceAll(s, "\n", "")
	if dec, err := base64.StdEncoding.DecodeString(s); err == nil {
		return dec, nil
	}
	return base64.RawStdEncoding.DecodeString(s)
}

// isHopByHop returns true for headers that should not be forwarded
// across a proxy. Lowercased comparison.
func isHopByHop(name string) bool {
	switch strings.ToLower(name) {
	case "connection", "keep-alive", "proxy-authenticate",
		"proxy-authorization", "te", "trailers",
		"transfer-encoding", "upgrade":
		return true
	}
	return false
}

// backoff is a tiny exponential backoff helper. Concurrency-safe is
// not required: the tunnel reconnects from one goroutine.
type backoff struct {
	current, max time.Duration
}

func newBackoff(start, max time.Duration) *backoff {
	return &backoff{current: start, max: max}
}

func (b *backoff) next() time.Duration {
	d := b.current
	b.current *= 2
	if b.current > b.max {
		b.current = b.max
	}
	return d
}
