package httpapi_test

import (
	"encoding/json"
	"testing"

	"github.com/ncecere/open-mcp-portal/internal/httpapi/apitypes"
	"github.com/ncecere/open-mcp-portal/internal/testutil"
)

// An upstream server's client credentials belong to the authorization
// server (issuer) they were registered at (MCP 2026-07-28, SEP-2352): when
// the server's resource metadata names another authorization server, saving
// its sign-in again registers the portal's client there instead of reusing
// the old one, and the portal's own registration says application_type web
// (SEP-837).
func TestUpstreamOAuthIssuerChangeReRegisters(t *testing.T) {
	e := newUpEnv(t, true)
	code, raw := e.createOAuth(e.editor, "gh", e.pm.URL(), map[string]any{"registration": "auto", "scopes": []string{"repo"}}, nil)
	if code != 201 {
		t.Fatalf("create = %d %s", code, raw)
	}
	var created struct{ Data apitypes.McpServerCreated }
	_ = json.Unmarshal(raw, &created)
	id := created.Data.Server.Id.String()
	first := e.get(e.editor, id).Oauth
	if first == nil || first.Registration != "dcr" || first.Issuer != e.as.Issuer() {
		t.Fatalf("first = %+v", first)
	}
	regs := e.as.Registrations()
	if len(regs) != 1 || regs[0]["application_type"] != "web" || regs[0]["token_endpoint_auth_method"] == nil {
		t.Fatalf("the portal's registration = %v", regs)
	}

	// Saving again with the same authorization server registers nothing.
	if code, ec, _ := e.patch(e.editor, id, map[string]any{"oauth": map[string]any{"registration": "auto"}}); code != 200 {
		t.Fatalf("resave = %d %s", code, ec)
	}
	if n := len(e.as.RegisteredClients()); n != 1 {
		t.Fatalf("a resave registered %d clients", n)
	}

	// The server now names another authorization server (another issuer).
	other := testutil.NewFakeOAuth(t)
	other.Set(func(f *testutil.FakeOAuth) {
		f.IssuerPath, f.IssParam, f.DCR, f.ScopesSupported = "/tenant-2", true, true, []string{"repo", "read:user", "write"}
	})
	e.pm.SetProtection(func(p *testutil.Protection) { p.Issuer = other.Issuer() })
	if code, ec, _ := e.patch(e.editor, id, map[string]any{"oauth": map[string]any{"registration": "auto"}}); code != 200 {
		t.Fatalf("resave after the change = %d %s", code, ec)
	}
	second := e.get(e.editor, id).Oauth
	if second.Issuer != other.Issuer() || second.Registration != "dcr" || second.ClientIdHint == first.ClientIdHint {
		t.Fatalf("after the change: %+v (was %+v)", second, first)
	}
	if got := other.RegisteredClients(); len(got) != 1 {
		t.Fatalf("the new authorization server has %d clients", len(got))
	}
	if r := other.Registrations(); len(r) != 1 || r[0]["application_type"] != "web" {
		t.Fatalf("registration at the new server = %v", r)
	}
	if n := len(e.as.RegisteredClients()); n != 1 {
		t.Fatalf("the old authorization server was registered at again (%d clients)", n)
	}

	// Back to the first one: its old client is not reused either.
	e.pm.SetProtection(func(p *testutil.Protection) { p.Issuer = e.as.Issuer() })
	if code, ec, _ := e.patch(e.editor, id, map[string]any{"oauth": map[string]any{"registration": "auto"}}); code != 200 {
		t.Fatalf("resave back = %d %s", code, ec)
	}
	third := e.get(e.editor, id).Oauth
	if third.Issuer != e.as.Issuer() || third.ClientIdHint == second.ClientIdHint || len(e.as.RegisteredClients()) != 2 {
		t.Fatalf("back: %+v (%d clients)", third, len(e.as.RegisteredClients()))
	}
}
