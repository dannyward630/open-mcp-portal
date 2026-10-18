package upstream

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ncecere/open-mcp-portal/internal/testutil"
)

func eraClient() *Client {
	c := testClient(true)
	c.Eras = NewEraCache()
	return c
}

func countMethod(f *testutil.FakeMCP, method string) (n int) {
	for _, r := range f.SeenRequests() {
		if r.Method == method {
			n++
		}
	}
	return n
}

func TestEraMatrix(t *testing.T) {
	for _, tc := range []struct {
		protocol testutil.Protocol
		era      Era
		version  string
		// initialize / discover requests the server saw (refused ones too):
		// the probe, and for a modern server the SDK session's own discover.
		initialize, discover int
	}{
		{testutil.ProtocolModern, EraModern, "2026-07-28", 0, 2},
		{testutil.ProtocolLegacy, EraLegacy, "2025-11-25", 1, 1},
		{testutil.ProtocolBoth, EraModern, "2026-07-28", 0, 2},
	} {
		t.Run(string(tc.protocol), func(t *testing.T) {
			fake := testutil.NewFakeMCP(t)
			fake.SetProtocol(tc.protocol)
			fake.EnableGatewayTools()
			c := eraClient()
			tgt := Target{URL: fake.URL(), ServerID: "s1", Timeout: 5 * time.Second}
			ctx := context.Background()

			d, err := c.Discover(ctx, tgt)
			if err != nil {
				t.Fatal(err)
			}
			if d.Protocol.Era != tc.era || d.Protocol.Version != tc.version || d.ProtocolVersion != tc.version {
				t.Fatalf("protocol = %+v, ProtocolVersion %q", d.Protocol, d.ProtocolVersion)
			}
			if n := CountKinds(d.Capabilities); n.Tools < 3 || n.Prompts != 1 || n.Resources != 1 {
				t.Fatalf("counts = %+v", n)
			}
			if got := countMethod(fake, "initialize"); got != tc.initialize {
				t.Errorf("initialize requests = %d, want %d", got, tc.initialize)
			}
			if got := countMethod(fake, "server/discover"); got != tc.discover {
				t.Errorf("server/discover requests = %d, want %d", got, tc.discover)
			}

			// Forwarded calls of every kind work in the era, and the era is
			// remembered: no new probe.
			before := countMethod(fake, "server/discover")
			res, _, err := c.CallTool(ctx, tgt, "echo", []byte(`{"text":"hi"}`))
			if err != nil || len(res.Content) != 1 {
				t.Fatalf("CallTool = %v, %v", res, err)
			}
			if _, _, err := c.GetPrompt(ctx, tgt, "incident_summary", map[string]string{"service": "vpn"}); err != nil {
				t.Fatalf("GetPrompt: %v", err)
			}
			if r, _, err := c.ReadResource(ctx, tgt, "status://services/email"); err != nil || len(r.Contents) != 1 {
				t.Fatalf("ReadResource = %v, %v", r, err)
			}
			if got := countMethod(fake, "server/discover"); got != before {
				t.Errorf("forwarded calls probed again: %d discover requests, had %d", got, before)
			}
			// What a modern call looks like on the wire.
			if tc.era == EraModern {
				var saw bool
				for _, r := range fake.SeenRequests() {
					if r.Method == "tools/call" {
						saw = true
						if r.Version != "2026-07-28" || r.McpMethod != "tools/call" {
							t.Errorf("tools/call headers = %+v", r)
						}
					}
				}
				if !saw {
					t.Error("the fake saw no tools/call")
				}
			}
		})
	}
}

// A server's own answer to the probe decides the era, per the streamable
// HTTP "Backward Compatibility" rule.
func TestEraFallbackRule(t *testing.T) {
	modernBody := func(code int) string {
		return `{"jsonrpc":"2.0","id":1,"error":{"code":` + strings.TrimSpace(strings.Repeat(" ", 0)) + itoa(code) + `,"message":"x"}}`
	}
	for _, tc := range []struct {
		name   string
		status int
		ctype  string
		body   string
		// want: the era learned, or the error class when the exchange fails
		// (a legacy fallback then hits the same handler's initialize).
		want  Era
		class string
	}{
		{"400 plain text", 400, "text/plain", "Bad Request: Unsupported protocol version", EraLegacy, ""},
		{"400 empty", 400, "", "", EraLegacy, ""},
		{"400 html", 400, "text/html", "<html>no</html>", EraLegacy, ""},
		{"400 other json rpc error", 400, "application/json", modernBody(-32600), EraLegacy, ""},
		{"400 header mismatch is modern", 400, "application/json", modernBody(-32020), "", ClassBadResponse},
		{"400 missing capability is modern", 400, "application/json", modernBody(-32021), "", ClassBadResponse},
		{"400 unsupported version with no list is modern", 400, "application/json", modernBody(-32022), "", ClassBadResponse},
		{"404 method not found is modern", 404, "application/json", modernBody(-32601), EraModern, ""},
		{"404 plain is a legacy attempt", 404, "text/plain", "404 page not found", EraLegacy, ""},
		{"405 plain is a legacy attempt", 405, "text/plain", "Method Not Allowed", EraLegacy, ""},
		{"401 is an error", 401, "text/plain", "no", "", ClassAuth},
		{"429 is an error", 429, "text/plain", "slow down", "", ClassRateLimited},
		{"500 is an error", 500, "text/plain", "boom", "", ClassUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if tc.ctype != "" {
					w.Header().Set("Content-Type", tc.ctype)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			c := eraClient()
			p, err := c.Negotiate(context.Background(), Target{URL: srv.URL + "/mcp", ServerID: "x", Timeout: 5 * time.Second})
			if tc.class != "" {
				var e *Error
				if !errors.As(err, &e) || e.Class != tc.class {
					t.Fatalf("err = %#v, want class %s", err, tc.class)
				}
				if requests.Load() != 1 {
					t.Fatalf("%d requests: an error must not fall back", requests.Load())
				}
				return
			}
			if err != nil || p.Era != tc.want {
				t.Fatalf("era = %+v, %v; want %s", p, err, tc.want)
			}
		})
	}
}

func itoa(n int) string {
	if n < 0 {
		return "-" + itoa(-n)
	}
	if n == 0 {
		return "0"
	}
	s := ""
	for ; n > 0; n /= 10 {
		s = string(rune('0'+n%10)) + s
	}
	return s
}

// A legacy HTTP+SSE server (404 or 405 for a POST, plain text) is the error it
// always was, not a fallback to the deprecated transport.
func TestDeprecatedSSEServerStaysAnError(t *testing.T) {
	for _, status := range []int{404, 405} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte("event: endpoint\ndata: /messages\n\n"))
				return
			}
			http.Error(w, "nope", status)
		}))
		_, err := eraClient().Discover(context.Background(), Target{URL: srv.URL + "/sse", ServerID: "x", Timeout: 5 * time.Second})
		var e *Error
		want := ClassNotFound
		if status == 405 {
			want = ClassUnavailable
		}
		if !errors.As(err, &e) || e.Class != want || e.HTTPStatus != status {
			t.Errorf("status %d: err = %#v", status, err)
		}
		srv.Close()
	}
}

func TestEraUnsupportedVersionNegotiation(t *testing.T) {
	handler := func(supported string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32022,"message":"unsupported","data":{"supported":` + supported + `}}}`))
		})
	}
	for _, tc := range []struct {
		supported string
		want      Protocol
		fail      bool
	}{
		// A stateful SDK server: a modern error that lists only older versions.
		{`["2025-11-25","2025-06-18"]`, Protocol{Era: EraLegacy, Version: "2025-11-25"}, false},
		{`["2024-11-05"]`, Protocol{Era: EraLegacy, Version: "2024-11-05"}, false},
		{`["1999-01-01"]`, Protocol{}, true},
	} {
		srv := httptest.NewServer(handler(tc.supported))
		p, err := eraClient().Negotiate(context.Background(), Target{URL: srv.URL, ServerID: "x", Timeout: 5 * time.Second})
		if tc.fail != (err != nil) || (!tc.fail && p != tc.want) {
			t.Errorf("supported %s: %+v, %v", tc.supported, p, err)
		}
		srv.Close()
	}

	// A server that only takes an older modern version than ours would be
	// retried with the newest version both speak.
	var versions []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		versions = append(versions, r.Header.Get("MCP-Protocol-Version"))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32022,"message":"unsupported","data":{"supported":["2026-07-28"]}}}`))
	}))
	defer srv.Close()
	_, err := eraClient().Negotiate(context.Background(), Target{URL: srv.URL, ServerID: "x", Timeout: 5 * time.Second})
	if err == nil || len(versions) != 2 {
		// The same version again can't help: one retry, then an error.
		if len(versions) > 2 {
			t.Errorf("versions tried = %v", versions)
		}
	}
}

func TestEraCache(t *testing.T) {
	fake := testutil.NewFakeMCP(t)
	fake.SetProtocol(testutil.ProtocolLegacy)
	fake.EnableGatewayTools()
	now := time.Now()
	c := eraClient()
	c.Eras.SetClock(func() time.Time { return now })
	ctx := context.Background()
	tgt := Target{URL: fake.URL(), ServerID: "s1", Timeout: 5 * time.Second}

	call := func(tg Target) {
		t.Helper()
		if _, _, err := c.CallTool(ctx, tg, "echo", []byte(`{"text":"x"}`)); err != nil {
			t.Fatal(err)
		}
	}
	call(tgt)
	call(tgt)
	if n := countMethod(fake, "server/discover"); n != 1 {
		t.Fatalf("discover probes = %d, want 1 (cached)", n)
	}

	// A new URL (same server) is learned again.
	moved := tgt
	moved.URL = fake.Server.URL + "/mcp2"
	call(moved)
	if n := countMethod(fake, "server/discover"); n != 2 {
		t.Fatalf("after a URL change: probes = %d, want 2", n)
	}
	// Another server with the same URL too.
	other := tgt
	other.ServerID = "s2"
	call(other)
	if n := countMethod(fake, "server/discover"); n != 3 {
		t.Fatalf("another server: probes = %d, want 3", n)
	}

	// The entry expires.
	now = now.Add(DefaultEraTTL + time.Second)
	call(tgt)
	if n := countMethod(fake, "server/discover"); n != 4 {
		t.Fatalf("after the TTL: probes = %d, want 4", n)
	}

	// A server that changes era under a cached entry is learned again by the
	// call that notices it (no failed call).
	fake.SetProtocol(testutil.ProtocolModern)
	call(tgt)
	p, err := c.Negotiate(ctx, tgt)
	if err != nil || p.Era != EraModern {
		t.Fatalf("after the server became modern: %+v, %v", p, err)
	}
}
