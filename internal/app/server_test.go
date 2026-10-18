package app

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// The main listener cuts slow headers and idle connections but has no
// ReadTimeout or WriteTimeout: those bound the whole exchange and would cut
// streamed (SSE) and long MCP responses.
func TestHTTPServerTimeouts(t *testing.T) {
	type key struct{}
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), key{}, "v"))
	srv := newHTTPServer(ctx, "127.0.0.1:0", http.NotFoundHandler())
	if srv.ReadHeaderTimeout != 10*time.Second {
		t.Errorf("ReadHeaderTimeout = %v, want 10s", srv.ReadHeaderTimeout)
	}
	if srv.IdleTimeout <= 0 || srv.IdleTimeout > 5*time.Minute {
		t.Errorf("IdleTimeout = %v, want a bound of a few minutes", srv.IdleTimeout)
	}
	if srv.ReadTimeout != 0 || srv.WriteTimeout != 0 {
		t.Errorf("ReadTimeout %v / WriteTimeout %v must stay unset: streams and long calls need long writes", srv.ReadTimeout, srv.WriteTimeout)
	}
	// Requests carry the process context's values but outlive its cancellation (a drain finishes them).
	base := srv.BaseContext(nil)
	cancel()
	if base.Value(key{}) != "v" || base.Err() != nil {
		t.Errorf("base context: value %v, err %v", base.Value(key{}), base.Err())
	}
}
