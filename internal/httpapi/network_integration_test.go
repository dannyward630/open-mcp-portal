package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/ncecere/open-mcp-portal/internal/config"
	"github.com/ncecere/open-mcp-portal/internal/httpapi"
	"github.com/ncecere/open-mcp-portal/internal/httpapi/apitypes"
	"github.com/ncecere/open-mcp-portal/internal/netpolicy"
	"github.com/ncecere/open-mcp-portal/internal/testutil"
)

const networkPath = "/v1/admin/settings/network"

func entry(kind, value string, allowHTTP bool) map[string]any {
	return map[string]any{"kind": kind, "value": value, "allowHttp": allowHTTP}
}

// The settings API: who reads and writes, If-Match, canonical entries,
// 422 invalid_entry, the deployment's read-only entries and the audit.
func TestNetworkSettingsAPI(t *testing.T) {
	deployment, err := netpolicy.ParseList("10.30.0.0/16,http:*.svc.cluster.local")
	if err != nil {
		t.Fatal(err)
	}
	app := newTestApp(t, func(c *config.Config) { c.UpstreamAllowedNetworks = deployment })
	admin, auditor, user := app.signIn("admin"), app.signIn("auditor"), app.signIn("user")

	var st apitypes.NetworkSettings
	if code := auditor.get(networkPath, &st); code != 200 || len(st.Entries) != 0 || st.Revision != 1 || len(st.DeploymentEntries) != 2 ||
		st.DeploymentEntries[1] != (apitypes.NetworkEntry{Kind: "host", Value: "*.svc.cluster.local", AllowHttp: true}) {
		t.Fatalf("auditor get = %d %+v", code, st)
	}
	if code, raw := user.raw("GET", networkPath, nil, nil); code != 403 {
		t.Fatalf("user get = %d %s", code, raw)
	}
	body := map[string]any{"entries": []any{entry("cidr", "10.20.0.0/16", false)}}
	code, ec := auditor.call("PUT", networkPath, body, nil, ifMatch(1))
	mustCode(t, "auditor put", code, ec, 403, "forbidden")
	code, ec = user.call("PUT", networkPath, body, nil, ifMatch(1))
	mustCode(t, "user put", code, ec, 403, "forbidden")
	code, ec = admin.call("PUT", networkPath, body, nil, nil)
	mustCode(t, "no If-Match", code, ec, 428, "revision_required")

	// Invalid entries: 422 invalid_entry with the index and the reason.
	many := make([]any, netpolicy.MaxEntries+1)
	for i := range many {
		many[i] = entry("cidr", fmt.Sprintf("10.%d.0.0/16", i), false)
	}
	for _, c := range []struct {
		entries []any
		index   float64
		reason  string
	}{
		{[]any{entry("cidr", "10.0.0.0/33", false)}, 0, "invalid_cidr"},
		{[]any{entry("cidr", "10.1.0.0/16", false), entry("host", "mcp.corp.example.com:8443", false)}, 1, "invalid_host"},
		{[]any{entry("host", "bücher.example", false)}, 0, "non_ascii"},
		{[]any{entry("host", "10.0.0.1", false)}, 0, "ip_as_host"},
		{[]any{entry("cidr", "127.0.0.0/8", false)}, 0, "always_blocked"},
		{[]any{entry("cidr", "169.254.169.254", false)}, 0, "always_blocked"},
		{[]any{entry("host", "localhost", true)}, 0, "always_blocked"},
		{[]any{entry("dns", "x", false)}, 0, "invalid_kind"},
		{[]any{entry("host", "a.example.com", false), entry("host", "A.example.com.", true)}, 1, "duplicate"},
		{[]any{map[string]any{"kind": "host", "value": "a.example.com", "allowHttp": false, "note": strings.Repeat("n", 201)}}, 0, "invalid_note"},
		{many, float64(netpolicy.MaxEntries), "too_many"},
	} {
		code, raw := admin.raw("PUT", networkPath, map[string]any{"entries": c.entries}, ifMatch(1))
		d := errorDetails(raw)
		if code != 422 || errorCode(raw) != "invalid_entry" || d["index"] != c.index || d["reason"] != c.reason {
			t.Errorf("%s: got %d %s, want 422 invalid_entry index %v", c.reason, code, raw, c.index)
		}
	}

	// Saved canonical; the ETag is the revision.
	note := "build farm"
	body = map[string]any{"entries": []any{
		map[string]any{"kind": "cidr", "value": "10.20.1.5/16", "allowHttp": false, "note": "  build farm "},
		entry("host", "*.Corp.Example.com.", true),
	}}
	code, raw := admin.raw("PUT", networkPath, body, ifMatch(1))
	if code != 200 {
		t.Fatalf("put = %d %s", code, raw)
	}
	var env struct{ Data apitypes.NetworkSettings }
	_ = json.Unmarshal(raw, &env)
	want := []apitypes.NetworkEntry{{Kind: "cidr", Value: "10.20.0.0/16", Note: &note}, {Kind: "host", Value: "*.corp.example.com", AllowHttp: true}}
	if st = env.Data; st.Revision != 2 || len(st.Entries) != 2 || st.Entries[0].Value != want[0].Value || st.Entries[0].Note == nil ||
		*st.Entries[0].Note != note || st.Entries[1] != want[1] || len(st.DeploymentEntries) != 2 {
		t.Fatalf("saved = %+v", st)
	}
	code, ec = admin.call("PUT", networkPath, body, nil, ifMatch(1))
	mustCode(t, "stale put", code, ec, 412, "revision_conflict")

	var log apitypes.AuditPage
	if code := admin.get("/v1/admin/audit?action=settings.network_update", &log); code != 200 || len(log.Items) != 1 ||
		log.Items[0].TargetLabel == nil || *log.Items[0].TargetLabel != "Internal networks" {
		t.Fatalf("audit = %d %+v", code, log)
	}
	var before, after string
	if err := app.Pool.QueryRow(context.Background(),
		`SELECT before_state::text, after_state::text FROM audit_log WHERE action = 'settings.network_update'`).Scan(&before, &after); err != nil {
		t.Fatal(err)
	}
	if before != `{"entries": []}` || !strings.Contains(after, `"value": "*.corp.example.com"`) || !strings.Contains(after, `"note": "build farm"`) {
		t.Fatalf("audit states = %s / %s", before, after)
	}
}

// internalEnv is a registry outside development mode (no allowance), where
// mcp.corp.example.com resolves to 10.20.0.5, rebind.corp.example.com to
// the cloud metadata address, and dialling 10.20.0.5 reaches the fake
// server (which listens on loopback, an address never allowed).
func internalEnv(t *testing.T) (*registryEnv, string) {
	// The SDK's DNS rebinding protection refuses a loopback listener reached
	// under another name; this server stands in for one at 10.20.0.5.
	_, h := testutil.NewFakeMCPHandler()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Host = listenerHost(r)
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	addr := srv.Listener.Addr().String()
	_, port, _ := net.SplitHostPort(addr)
	e := newRegistryEnvWith(t, func(d *httpapi.Deps, _ *testutil.FakeMCP) {
		c := d.Registry.Client()
		c.AllowPrivate = false
		c.Lookup = func(_ context.Context, host string) ([]netip.Addr, error) {
			switch strings.TrimSuffix(strings.ToLower(host), ".") {
			case "mcp.corp.example.com":
				return []netip.Addr{netip.MustParseAddr("10.20.0.5")}, nil
			case "rebind.corp.example.com":
				return []netip.Addr{netip.MustParseAddr("169.254.169.254")}, nil
			}
			return nil, errors.New("no such host")
		}
		c.Dial = func(ctx context.Context, network, address string) (net.Conn, error) {
			if address != net.JoinHostPort("10.20.0.5", port) {
				return nil, fmt.Errorf("unexpected dial to %s", address)
			}
			var nd net.Dialer
			return nd.DialContext(ctx, network, addr)
		}
	})
	return e, port
}

// listenerHost is the loopback host a request's server listens on.
func listenerHost(r *http.Request) string {
	if a, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok {
		return a.String()
	}
	return "127.0.0.1"
}

func (e *registryEnv) setNetworks(entries ...any) {
	e.t.Helper()
	var st apitypes.NetworkSettings
	if code := e.admin.get(networkPath, &st); code != 200 {
		e.t.Fatalf("get networks = %d", code)
	}
	if entries == nil {
		entries = []any{}
	}
	code, ec := e.admin.call("PUT", networkPath, map[string]any{"entries": entries}, nil, ifMatch(st.Revision))
	mustCode(e.t, "put networks", code, ec, 200, "")
}

// Registering an internal server over plain http works once its host is
// allowed; removing the entry makes its next exchange fail (not_allowed).
func TestInternalServerAllowlist(t *testing.T) {
	e, port := internalEnv(t)
	base := toolsTeam + "/mcp-servers"
	httpURL := "http://mcp.corp.example.com:" + port + "/mcp"

	refusal := func(url string) (string, string) {
		t.Helper()
		code, raw := e.editor.raw("POST", base, map[string]any{"slug": "refused", "name": "Refused", "url": url}, nil)
		if code != 422 || errorCode(raw) != "url_not_allowed" {
			t.Fatalf("%s = %d %s, want 422 url_not_allowed", url, code, raw)
		}
		var env struct{ Error struct{ Message string } }
		_ = json.Unmarshal(raw, &env)
		reason, _ := errorDetails(raw)["reason"].(string)
		return reason, env.Error.Message
	}
	// Before any entry: http is refused, internal literals too.
	if r, _ := refusal(httpURL); r != "scheme" {
		t.Fatalf("http before the entry: %s", r)
	}
	if r, msg := refusal("https://10.20.0.5:" + port + "/mcp"); r != "private_address" || !strings.Contains(msg, "platform admin") {
		t.Fatalf("private literal: %s %q", r, msg)
	}
	if r, _ := refusal("https://169.254.169.254/latest"); r != "blocked_address" {
		t.Fatalf("metadata literal: %s", r)
	}
	if r, _ := refusal("http://localhost:" + port + "/mcp"); r != "scheme" {
		t.Fatalf("localhost http: %s", r)
	}
	// A name is saved, and refused when dialled (it resolves to an internal address).
	byName := e.create(map[string]any{"slug": "by-name", "name": "By name", "url": "https://mcp.corp.example.com:" + port + "/mcp"})
	if byName.RefreshError == nil || byName.RefreshError.ErrorClass == nil || *byName.RefreshError.ErrorClass != "not_allowed" {
		t.Fatalf("internal name without an entry = %+v", byName.RefreshError)
	}

	e.setNetworks(entry("host", "mcp.corp.example.com", true), entry("host", "rebind.corp.example.com", true))
	created := e.create(map[string]any{"slug": "internal", "name": "Internal", "url": httpURL})
	if created.Refresh == nil || created.RefreshError != nil || created.Server.Health.Status != "healthy" {
		t.Fatalf("allowlisted http server = %+v / %+v", created.Refresh, created.RefreshError)
	}
	id := created.Server.Id.String()
	// DNS pointing at the metadata address stays blocked, host entry or not.
	rebind := e.create(map[string]any{"slug": "rebind", "name": "Rebind", "url": "http://rebind.corp.example.com:" + port + "/mcp"})
	if rebind.RefreshError == nil || rebind.RefreshError.ErrorClass == nil || *rebind.RefreshError.ErrorClass != "not_allowed" {
		t.Fatalf("rebind to metadata = %+v", rebind.RefreshError)
	}

	// The checker agrees, without connecting.
	check := func(s *session, url string) apitypes.NetworkCheckResult {
		t.Helper()
		var out apitypes.NetworkCheckResult
		code, ec := s.call("POST", networkPath+"/check", map[string]any{"url": url}, &out, nil)
		mustCode(t, "check "+url, code, ec, 200, "")
		return out
	}
	r := check(e.auditor, httpURL)
	if !r.Allowed || r.Reason != "host_entry" || r.MatchedEntry == nil || r.MatchedEntry.Value != "mcp.corp.example.com" ||
		r.ResolvedAddresses == nil || len(*r.ResolvedAddresses) != 1 ||
		(*r.ResolvedAddresses)[0] != (apitypes.NetworkCheckAddress{Address: "10.20.0.5", Allowed: true, Reason: "host_entry"}) {
		t.Fatalf("check allowed = %+v", r)
	}
	if r := check(e.admin, "http://rebind.corp.example.com/mcp"); r.Allowed || r.Reason != "blocked_address" ||
		r.ResolvedAddresses == nil || (*r.ResolvedAddresses)[0].Reason != "blocked_address" {
		t.Fatalf("check rebind = %+v", r)
	}
	if r := check(e.admin, "https://10.20.0.5/mcp"); r.Allowed || r.Reason != "private_address" {
		t.Fatalf("check literal = %+v", r)
	}
	if r := check(e.admin, "https://nowhere.corp.example.com/mcp"); r.Allowed || r.Reason != "dns_failed" {
		t.Fatalf("check unresolvable = %+v", r)
	}
	if r := check(e.admin, "ftp://mcp.corp.example.com/mcp"); r.Allowed || r.Reason != "scheme" {
		t.Fatalf("check scheme = %+v", r)
	}
	code, ec := e.owner.call("POST", networkPath+"/check", map[string]any{"url": httpURL}, nil, nil)
	mustCode(t, "team owner check", code, ec, 403, "forbidden")

	// A literal address in a cidr entry with allowHttp.
	e.setNetworks(entry("host", "mcp.corp.example.com", true), map[string]any{"kind": "cidr", "value": "10.20.0.0/16", "allowHttp": true})
	literal := e.create(map[string]any{"slug": "literal", "name": "Literal", "url": "http://10.20.0.5:" + port + "/mcp"})
	if literal.RefreshError != nil {
		t.Fatalf("cidr literal = %+v", literal.RefreshError)
	}
	if r := check(e.auditor, "http://10.20.0.5/mcp"); !r.Allowed || r.Reason != "cidr_entry" || r.MatchedEntry == nil || r.MatchedEntry.Value != "10.20.0.0/16" {
		t.Fatalf("check cidr = %+v", r)
	}

	// Removing the entries edits no server: the next exchange fails.
	e.setNetworks()
	var res apitypes.McpTestResult
	code, ec = e.editor.call("POST", base+"/"+id+"/test", nil, &res, nil)
	mustCode(t, "test after removal", code, ec, 200, "")
	if res.Ok || res.ErrorClass == nil || *res.ErrorClass != "not_allowed" {
		t.Fatalf("test after removal = %+v", res)
	}
	sv := e.server(e.editor, id)
	if sv.Url != httpURL || sv.Health.Status != "failing" || sv.Health.ErrorClass == nil || *sv.Health.ErrorClass != "not_allowed" {
		t.Fatalf("server after removal = %+v", sv)
	}
}
