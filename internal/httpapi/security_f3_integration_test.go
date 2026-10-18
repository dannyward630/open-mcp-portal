package httpapi_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/ncecere/open-mcp-portal/internal/httpapi/apitypes"
	"github.com/ncecere/open-mcp-portal/internal/testutil"
)

// F-3 (docs/threat-model.md, decision 60): a person's platform-provider token
// goes only to a server they allowed, at the origin they allowed it, for the
// scopes they were shown. A server's URL moving to another origin, or its
// provider changing, ends the grants; a consent link is for one server.

const f3Tool = "status__provider_token"

// myGrants are the servers blair's connection to the provider is allowed for.
func (e *provEnv) myGrants() []apitypes.ProviderServerGrant {
	e.t.Helper()
	var conns []apitypes.ProviderConnection
	if code := e.member.get("/v1/me/connections", &conns); code != 200 {
		e.t.Fatalf("connections = %d", code)
	}
	for _, c := range conns {
		if c.Provider.Slug == e.provider.Slug {
			return c.AllowedServers
		}
	}
	return nil
}

func (e *provEnv) grantCount(serverID string) string {
	e.t.Helper()
	return e.scalar(`SELECT count(*)::text FROM provider_server_grants WHERE server_id = $1`, serverID)
}

// allow confirms a link as blair without going to the provider.
func (e *provEnv) allow(link string) apitypes.ConnectConfirmed {
	e.t.Helper()
	var out apitypes.ConnectConfirmed
	code, ec := e.member.call("POST", "/v1/connect/requests/"+requestID(link)+"/confirm", nil, &out, nil)
	mustCode(e.t, "confirm "+link, code, ec, 200, "")
	return out
}

func (e *provEnv) confirmCode(s *session, link string) (int, string) {
	e.t.Helper()
	return s.call("POST", "/v1/connect/requests/"+requestID(link)+"/confirm", nil, nil, nil)
}

// connectBroadly connects blair to the provider ahead of any call (scopes
// asked for by hand, no consent link: no server is allowed by it).
func (e *provEnv) connectBroadly(scopes ...string) {
	e.t.Helper()
	if to := e.connect(e.member, map[string]any{"providerId": e.provider.Id, "scopes": scopes}); !strings.Contains(to, "result=connected") {
		e.t.Fatalf("connect = %s", to)
	}
}

// addServer adds a second server of the tools team on fake, requiring the
// provider with scopes, to the support gateway; its tool is <slug>__provider_token.
func (e *provEnv) addServer(slug string, fake *testutil.FakeMCP, scopes ...string) apitypes.McpServer {
	e.t.Helper()
	sv := e.createServer(e.owner, "tools", slug, fake.URL(), nil)
	e.setServerRequirement(sv, scopes...)
	e.mustSetCaps(e.owner, toolsTeam+"/gateways/"+e.gateway.Id.String(), sel(
		e.capID(e.owner, e.server, "tool", "provider_token"), e.capID(e.owner, e.server, "tool", "whoami"),
		e.capID(e.owner, e.server, "tool", "echo"), e.capID(e.owner, e.server, "resource", "status://services"),
		e.capID(e.owner, sv, "tool", "provider_token")))
	return sv
}

func (e *provEnv) setServerRequirement(sv apitypes.McpServer, scopes ...string) {
	e.t.Helper()
	e.patchServerReq(sv, map[string]any{"providerId": e.provider.Id, "scopes": scopes})
}

func (e *provEnv) patchServerReq(sv apitypes.McpServer, req any) {
	e.t.Helper()
	e.patchServer(e.owner, sv, map[string]any{"requirement": req})
}

// noSecretsAnywhere checks that no token or client secret reached the API
// bodies read so far, the audit log (also the grants' entries), the call
// records, the grants table or the logs.
func (e *provEnv) noSecretsAnywhere() {
	e.t.Helper()
	secrets := append(append(e.oauth.Issued(), e.oauth.IssuedRefresh()...), e.oauth.ClientSecret)
	dump := e.scalar(`SELECT coalesce(string_agg(row_to_json(a)::text, ' '), '') FROM audit_log a`) +
		e.scalar(`SELECT coalesce(string_agg(row_to_json(c)::text, ' '), '') FROM mcp_calls c`) +
		e.scalar(`SELECT coalesce(string_agg(row_to_json(g)::text, ' '), '') FROM provider_server_grants g`) +
		e.scalar(`SELECT coalesce(string_agg(row_to_json(r)::text, ' '), '') FROM provider_connect_requests r`) +
		e.logs.String() + strings.Join(e.responses, " ")
	for _, p := range []string{"/v1/me/connections", "/v1/admin/users/" + e.userID("blair@localhost") + "/connections", "/v1/admin/audit?limit=200"} {
		_, raw := e.admin.raw("GET", p, nil, nil)
		dump += string(raw)
	}
	for _, sec := range secrets {
		if strings.Contains(dump, sec) {
			e.t.Fatalf("a token or secret leaked (%d chars)", len(sec))
		}
	}
}

// The review's reproduction: a member connects the provider and calls a tool
// of the legitimate server; an editor re-points the server at a second host;
// the member's next call must not carry the token there.
func TestProviderTokenFollowsURLChange(t *testing.T) {
	e := newProvEnv(t)
	evil := e.otherFake

	// The member consents for the server (a consent link, the provider round
	// trip), and the call reaches the legitimate server with the token.
	link := e.consentURL(e.callTool(e.key, f3Tool), "not_connected")
	if to := e.connect(e.member, map[string]any{"providerId": e.provider.Id, "requestId": requestID(link)}); !strings.Contains(to, "result=connected") {
		t.Fatalf("connect = %s", to)
	}
	if rep := e.providerReport(e.callTool(e.key, f3Tool)); !rep.Received || rep.Provider != "github" {
		t.Fatalf("report = %+v", rep)
	}
	if e.fake.ProviderTokenRequests() == 0 {
		t.Fatal("the server the person allowed didn't get the token")
	}
	grants := e.myGrants()
	if len(grants) != 1 || grants[0].Server.Slug != "status" || grants[0].Server.Name != "STATUS" || grants[0].Server.TeamSlug != "tools" ||
		grants[0].Server.TeamName != "TOOLS" || grants[0].Origin != originOfURL(e.fake.URL()) || !slices.Equal(grants[0].Scopes, []string{"repo"}) {
		t.Fatalf("grants = %+v", grants)
	}
	readyMark := func() bool {
		var detail apitypes.MyGatewayDetail
		if code := e.member.get("/v1/me/gateways/"+e.gateway.Id.String(), &detail); code != 200 {
			t.Fatalf("my gateway = %d", code)
		}
		for _, c := range detail.Capabilities {
			if c.Name == "provider_token" && c.Requires != nil && c.Requires.Connected != nil {
				return *c.Requires.Connected
			}
		}
		t.Fatal("no requirement on provider_token")
		return false
	}
	if !readyMark() {
		t.Fatal("My gateways doesn't show the capability as ready")
	}

	// The editor re-points the server at a host of their own.
	e.patchServer(e.editor, e.server, map[string]any{"url": evil.URL()})
	out := e.callTool(e.key, f3Tool)
	link2 := e.consentURL(out, "server_not_allowed")
	if n := e.toolCalls("provider_token"); n != 1 { // blair's one call to the first host
		t.Fatalf("provider_token reached a server %d times", n)
	}
	if evil.ProviderTokenRequests() != 0 {
		t.Fatalf("the second host got the token (%d requests)", evil.ProviderTokenRequests())
	}
	for _, c := range evil.ToolCalls() {
		if c.Tool == "provider_token" || c.Header.Get("MCP-Gateway-Provider-Token") != "" || c.Header.Get("MCP-Gateway-Provider") != "" {
			t.Fatalf("the second host saw a call: %+v", c)
		}
	}
	if readyMark() {
		t.Fatal("My gateways shows the moved server as ready")
	}
	if g := e.myGrants(); len(g) != 0 {
		t.Fatalf("grants after the move = %+v", g)
	}
	if v := e.scalar(`SELECT metadata->>'reason' || ' ' || (metadata->>'grantsDeleted') || ' ' || (metadata->>'oldOrigin') || ' ' || (metadata->>'newOrigin')
		FROM audit_log WHERE action = 'mcp_server.provider_grants_reset' AND target_id = $1`, e.server.Id.String()); v !=
		"origin_changed 1 "+originOfURL(e.fake.URL())+" "+originOfURL(evil.URL()) {
		t.Fatalf("grants reset audit = %q", v)
	}

	// The consent page names the server and its team, the account and the scopes.
	var cr apitypes.ConnectRequest
	if code := e.member.get("/v1/connect/requests/"+requestID(link2), &cr); code != 200 {
		t.Fatalf("request = %d", code)
	}
	if cr.Reason != "server_not_allowed" || cr.Server == nil || cr.Server.Name != "STATUS" || cr.Server.TeamSlug != "tools" || cr.Server.TeamName != "TOOLS" ||
		!slices.Equal(cr.RequiredScopes, []string{"repo"}) || !slices.Equal(cr.GrantedScopes, []string{"repo"}) {
		t.Fatalf("request view = %+v", cr)
	}

	// The person allows the new address (no trip to the provider), and the call goes there with the token.
	tokenReqs, authorizes := e.oauth.TokenRequests(), e.oauth.AuthorizeRequests()
	if c := e.allow(link2); c.Server.Name != "STATUS" || !slices.Equal(c.Scopes, []string{"repo"}) || c.Provider.Slug != "github" {
		t.Fatalf("confirmed = %+v", c)
	}
	if e.oauth.TokenRequests() != tokenReqs || e.oauth.AuthorizeRequests() != authorizes {
		t.Fatal("allowing a server contacted the provider")
	}
	if rep := e.providerReport(e.callTool(e.key, f3Tool)); !rep.Received {
		t.Fatalf("report after consent = %+v", rep)
	}

	// A path-only edit keeps the grant: no new consent, the audit has no second reset.
	e.patchServer(e.editor, e.server, map[string]any{"url": evil.URL() + "/v2"})
	if rep := e.providerReport(e.callTool(e.key, f3Tool)); !rep.Received {
		t.Fatalf("report after a path-only edit = %+v", rep)
	}
	if n := e.scalar(`SELECT count(*)::text FROM audit_log WHERE action = 'mcp_server.provider_grants_reset'`); n != "1" || e.grantCount(e.server.Id.String()) != "1" {
		t.Fatalf("a path-only edit reset grants (%s resets, %s grants)", n, e.grantCount(e.server.Id.String()))
	}

	// Belt and braces: a grant whose stored origin isn't the server's now is
	// never honoured, whatever happened to the reset.
	e.scalar(`UPDATE provider_server_grants SET origin = 'https://elsewhere.example' RETURNING 'x'`)
	before := evil.ProviderTokenRequests()
	e.consentURL(e.callTool(e.key, f3Tool), "server_not_allowed")
	if evil.ProviderTokenRequests() != before {
		t.Fatal("a grant for another origin was honoured")
	}
	e.noSecretsAnywhere()
}

// A second server that requires the same provider with fewer scopes gets a
// consent prompt, not the token the person gave another server, and only the
// server the person allowed gets a grant.
func TestSecondServerWithFewerScopes(t *testing.T) {
	e := newProvEnv(t)
	e.setRequirement(map[string]any{"providerId": e.provider.Id, "scopes": []string{"repo", "read:org"}})
	link := e.consentURL(e.callTool(e.key, f3Tool), "not_connected")
	e.connect(e.member, map[string]any{"providerId": e.provider.Id, "requestId": requestID(link)})
	if out := e.callTool(e.key, f3Tool); out["result"] == nil {
		t.Fatalf("call of the first server = %v", out)
	}
	second := testutil.NewFakeMCP(t)
	second.EnableGatewayTools()
	b := e.addServer("second", second, "repo")

	// The connection covers repo, but nobody allowed this server.
	out := e.callTool(e.key, "second__provider_token")
	linkB := e.consentURL(out, "server_not_allowed")
	if _, msg, _ := rpcErr(out); !strings.Contains(msg, "allow SECOND (tools) to use your Github account (repo)") {
		t.Fatalf("message = %q", msg)
	}
	if second.ProviderTokenRequests() != 0 || e.toolCalls("provider_token") != 1 {
		t.Fatal("the second server got a call or the token without consent")
	}
	var cr apitypes.ConnectRequest
	if code := e.member.get("/v1/connect/requests/"+requestID(linkB), &cr); code != 200 || cr.Server == nil || cr.Server.Slug != "second" ||
		!slices.Equal(cr.GrantedScopes, []string{"repo", "read:org"}) || !slices.Equal(cr.RequiredScopes, []string{"repo"}) {
		t.Fatalf("request = %d %+v", code, cr)
	}
	e.allow(linkB)
	grants := e.myGrants() // by server name: SECOND, STATUS
	if len(grants) != 2 || grants[0].Server.Slug != "second" || !slices.Equal(grants[0].Scopes, []string{"repo"}) ||
		grants[1].Server.Slug != "status" || !slices.Equal(grants[1].Scopes, []string{"repo", "read:org"}) {
		t.Fatalf("grants = %+v", grants)
	}
	if rep := e.providerReport(e.callTool(e.key, "second__provider_token")); !rep.Received || second.ProviderTokenRequests() != 1 {
		t.Fatalf("report = %+v", rep)
	}

	// The second server's requirement widens to what the first one has: its grant is for repo only.
	e.setServerRequirement(b, "repo", "read:org")
	linkB2 := e.consentURL(e.callTool(e.key, "second__provider_token"), "server_not_allowed")
	e.allow(linkB2)
	if grants = e.myGrants(); len(grants) != 2 || !slices.Equal(grants[0].Scopes, []string{"repo", "read:org"}) {
		t.Fatalf("grants after widening = %+v", grants)
	}
	if n := e.scalar(`SELECT count(*)::text FROM audit_log WHERE action = 'provider_connection.server_allow'`); n != "3" {
		t.Fatalf("server_allow audits = %s", n)
	}
	e.noSecretsAnywhere()
}

// A consent link is made for one server, at one origin: it can allow only that
// server, and not after the server moved. Links of others, used or expired
// ones fail as before.
func TestConnectLinkIsForOneServer(t *testing.T) {
	e := newProvEnv(t)
	second := testutil.NewFakeMCP(t)
	second.EnableGatewayTools()
	b := e.addServer("second", second, "repo")

	linkA := e.consentURL(e.callTool(e.key, f3Tool), "not_connected")
	linkB := e.consentURL(e.callTool(e.key, "second__provider_token"), "not_connected")
	if linkA == linkB {
		t.Fatal("two servers share one consent link")
	}
	var cr apitypes.ConnectRequest
	e.member.get("/v1/connect/requests/"+requestID(linkB), &cr)
	if cr.Server == nil || cr.Server.Id != b.Id {
		t.Fatalf("link B names %+v", cr.Server)
	}

	// Link A, through the provider round trip, allows A and nothing else.
	e.connect(e.member, map[string]any{"providerId": e.provider.Id, "requestId": requestID(linkA)})
	if e.grantCount(e.server.Id.String()) != "1" || e.grantCount(b.Id.String()) != "0" {
		t.Fatalf("grants: A %s, B %s", e.grantCount(e.server.Id.String()), e.grantCount(b.Id.String()))
	}
	if out := e.callTool(e.key, f3Tool); out["result"] == nil {
		t.Fatalf("A = %v", out)
	}
	if got := e.consentURL(e.callTool(e.key, "second__provider_token"), "server_not_allowed"); got != linkB {
		t.Fatalf("B's link changed: %s, was %s", got, linkB)
	}
	if second.ProviderTokenRequests() != 0 {
		t.Fatal("B got the token")
	}
	// Link B's own page says what it now is.
	if code := e.member.get("/v1/connect/requests/"+requestID(linkB), &cr); code != 200 || cr.Reason != "server_not_allowed" {
		t.Fatalf("link B = %d %+v", code, cr)
	}

	// Another person can't use it; once it's used it is gone; an expired one is too.
	code, ec := e.confirmCode(e.editor, linkB)
	mustCode(t, "another person's link", code, ec, 404, "connect_request_not_found")
	if e.grantCount(b.Id.String()) != "0" {
		t.Fatal("another person's confirm allowed the server")
	}
	e.allow(linkB)
	code, ec = e.confirmCode(e.member, linkB)
	mustCode(t, "used link", code, ec, 410, "connect_request_expired")
	if e.grantCount(b.Id.String()) != "1" {
		t.Fatal("B isn't allowed after its own link")
	}
	// (the matrix classifies the route; an API key is refused by the session gate, covered there)

	// B moves to another host after the person opened its link: the link is for the old origin.
	e.patchServer(e.owner, b, map[string]any{"url": e.fake.URL()})
	if e.grantCount(b.Id.String()) != "0" {
		t.Fatal("the move kept B's grant")
	}
	linkB2 := e.consentURL(e.callTool(e.key, "second__provider_token"), "server_not_allowed")
	e.patchServer(e.owner, b, map[string]any{"url": second.URL()})
	code, ec = e.confirmCode(e.member, linkB2)
	mustCode(t, "link for the old origin", code, ec, 410, "connect_request_server_changed")
	code, ec, _ = e.start(e.member, map[string]any{"providerId": e.provider.Id, "requestId": requestID(linkB2)})
	mustCode(t, "start with a link for the old origin", code, ec, 410, "connect_request_server_changed")
	code, ec = e.member.call("GET", "/v1/connect/requests/"+requestID(linkB2), nil, nil, nil)
	mustCode(t, "view of a link for the old origin", code, ec, 410, "connect_request_server_changed")
	if e.grantCount(b.Id.String()) != "0" {
		t.Fatal("a stale link allowed the server")
	}

	// The same for a provider round trip that began before the move: connected, nothing allowed.
	linkB3 := e.consentURL(e.callTool(e.key, "second__provider_token"), "server_not_allowed")
	code, ec, st := e.start(e.member, map[string]any{"providerId": e.provider.Id, "requestId": requestID(linkB3)})
	mustCode(t, "start", code, ec, 200, "")
	back := providerAuthorize(t, e.member, st.AuthorizeUrl)
	e.patchServer(e.owner, b, map[string]any{"url": e.fake.URL()})
	if to := callback(t, e.member, back); to != "/connect/done?provider=github&result=expired" {
		t.Fatalf("callback after the move = %s", to)
	}
	if e.grantCount(b.Id.String()) != "0" {
		t.Fatal("a callback after the move allowed the server")
	}
	if code, _ := e.member.raw("GET", "/v1/me/connections", nil, nil); code != 200 {
		t.Fatal("connections")
	}
	// An expired link.
	e.patchServer(e.owner, b, map[string]any{"url": second.URL()})
	linkB4 := e.consentURL(e.callTool(e.key, "second__provider_token"), "server_not_allowed")
	e.scalar(`UPDATE provider_connect_requests SET expires_at = now() - interval '1 second' RETURNING 'x'`)
	code, ec = e.confirmCode(e.member, linkB4)
	mustCode(t, "expired link", code, ec, 410, "connect_request_expired")
	e.noSecretsAnywhere()
}

// A person who is already connected with the scopes confirms a new server:
// nothing reaches the provider, and the grant is recorded. Not connected, or
// scopes missing: the person goes to the provider instead (409), and the link
// stays usable.
func TestConfirmSkipsTheProviderRoundTrip(t *testing.T) {
	e := newProvEnv(t)
	link0 := e.consentURL(e.callTool(e.key, f3Tool), "not_connected")

	code, ec := e.confirmCode(e.member, link0)
	mustCode(t, "confirm without a connection", code, ec, 409, "connect_required")
	var cr apitypes.ConnectRequest
	if code := e.member.get("/v1/connect/requests/"+requestID(link0), &cr); code != 200 || cr.Reason != "not_connected" {
		t.Fatalf("the link after a refused confirm = %d %+v", code, cr)
	}

	// Connected broadly: the same link now only needs the server's consent.
	e.connectBroadly("repo")
	if code := e.member.get("/v1/connect/requests/"+requestID(link0), &cr); code != 200 || cr.Reason != "server_not_allowed" ||
		!slices.Equal(cr.GrantedScopes, []string{"repo"}) || cr.AccountLabel == nil || *cr.AccountLabel != "octocat" {
		t.Fatalf("link after connecting = %d %+v", code, cr)
	}
	authorizes, tokenReqs, tokens := e.oauth.AuthorizeRequests(), e.oauth.TokenRequests(), len(e.oauth.Issued())
	c := e.allow(link0)
	if c.Server.Slug != "status" || !slices.Equal(c.Scopes, []string{"repo"}) {
		t.Fatalf("confirmed = %+v", c)
	}
	if e.oauth.AuthorizeRequests() != authorizes || e.oauth.TokenRequests() != tokenReqs || len(e.oauth.Issued()) != tokens {
		t.Fatal("the provider was contacted")
	}
	if g := e.myGrants(); len(g) != 1 || g[0].Server.Slug != "status" {
		t.Fatalf("grants = %+v", g)
	}
	rep := e.providerReport(e.callTool(e.key, f3Tool))
	issued := e.oauth.Issued()
	if !rep.Received || rep.TokenSHA256 != testutil.TokenDigest(issued[len(issued)-1]) {
		t.Fatalf("report = %+v", rep)
	}
	if v := e.scalar(`SELECT metadata->>'server' || ' ' || (metadata->>'origin') || ' ' || (metadata->'scopes')::text || ' ' || (metadata->>'viaRequest')
		FROM audit_log WHERE action = 'provider_connection.server_allow'`); v != `STATUS `+originOfURL(e.fake.URL())+` ["repo"] true` {
		t.Fatalf("grant audit = %q", v)
	}

	// Scopes the account doesn't have: the person is sent to the provider (the union), not given a grant.
	e.setRequirement(map[string]any{"providerId": e.provider.Id, "scopes": []string{"repo", "read:org"}})
	link1 := e.consentURL(e.callTool(e.key, f3Tool), "missing_scopes")
	code, ec = e.confirmCode(e.member, link1)
	mustCode(t, "confirm with missing scopes", code, ec, 409, "connect_required")
	if g := e.myGrants(); len(g) != 1 || !slices.Equal(g[0].Scopes, []string{"repo"}) {
		t.Fatalf("grants after a refused confirm = %+v", g)
	}
	// ...and the round trip records the grant at the callback, for the scopes shown.
	e.connect(e.member, map[string]any{"providerId": e.provider.Id, "requestId": requestID(link1)})
	if g := e.myGrants(); len(g) != 1 || !slices.Equal(g[0].Scopes, []string{"repo", "read:org"}) {
		t.Fatalf("grants after the round trip = %+v", g)
	}
	// A connection that needs renewing can't be confirmed against.
	e.scalar(`UPDATE provider_connections SET status = 'needs_reconsent', access_token_cipher = NULL, refresh_token_cipher = NULL, expires_at = NULL
		RETURNING 'x'`)
	link2 := e.consentURL(e.callTool(e.key, f3Tool), "needs_reconsent")
	code, ec = e.confirmCode(e.member, link2)
	mustCode(t, "confirm with a connection that needs renewing", code, ec, 409, "connect_required")
	e.noSecretsAnywhere()
}

// The consent answers of the 2026-07-28 work (decision 59) carry the new
// reason: legacy -32042, modern URL elicitation, modern without it.
func TestServerNotAllowedConsentShapes(t *testing.T) {
	e := newProvEnv(t)
	e.connectBroadly("repo")
	msg := "Allow STATUS (tools) to use your Github account (repo) for " + f3Tool + " in the SUPPORT gateway."

	out := e.callTool(e.key, f3Tool)
	legacy := e.consentURL(out, "server_not_allowed")
	if _, text, _ := rpcErr(out); !strings.Contains(text, "allow STATUS (tools) to use your Github account (repo)") {
		t.Fatalf("legacy message = %q", text)
	}
	if els := out["error"].(map[string]any)["data"].(map[string]any)["elicitations"].([]any); els[0].(map[string]any)["message"] != msg {
		t.Fatalf("elicitation = %v", els)
	}

	modern := e.mcall(e.key, "tools/call", toolCall(f3Tool, nil), urlCaps)
	link, _ := e.inputRequired(modern, msg)
	if link != legacy {
		t.Fatalf("modern link %s, legacy %s", link, legacy)
	}
	e.lastCall("needs_authorization server_not_allowed github")

	plain := e.mcall(e.key, "tools/call", toolCall(f3Tool, nil), map[string]any{})
	if l := plainConsentLink(t, plain, e.app.URL, e.provider.Slug); l != legacy {
		t.Fatalf("plain link %s", l)
	}
	if text := resultOf(t, plain)["content"].([]any)[0].(map[string]any)["text"].(string); !strings.Contains(text, "allow STATUS (tools) to use your Github account") {
		t.Fatalf("plain text = %q", text)
	}
	e.lastCall("needs_authorization server_not_allowed github")
	if e.toolCalls("provider_token") != 0 || e.fake.ProviderTokenRequests() != 0 {
		t.Fatal("something reached the server without the person's consent")
	}

	// Allowing the server turns the same call into a result for all three eras.
	e.allow(legacy)
	if out := e.callTool(e.key, f3Tool); out["result"] == nil {
		t.Fatalf("legacy after consent = %v", out)
	}
	if got := e.mcall(e.key, "tools/call", toolCall(f3Tool, nil), urlCaps); resultOf(t, got)["resultType"] != "complete" {
		t.Fatalf("modern after consent = %v", got)
	}
}

// Grants end with what they were for: a changed provider, a deleted server,
// the connection (a disconnect, a provider's or a person's delete).
func TestProviderGrantsReset(t *testing.T) {
	e := newProvEnv(t)
	second := testutil.NewFakeMCP(t)
	second.EnableGatewayTools()
	b := e.addServer("second", second, "repo")
	linkA := e.consentURL(e.callTool(e.key, f3Tool), "not_connected")
	e.connect(e.member, map[string]any{"providerId": e.provider.Id, "requestId": requestID(linkA)})
	e.allow(e.consentURL(e.callTool(e.key, "second__provider_token"), "server_not_allowed"))
	if len(e.myGrants()) != 2 {
		t.Fatalf("grants = %+v", e.myGrants())
	}

	// The server requires another provider: its grant goes, and the old provider needs consent again.
	otherOAuth := testutil.NewFakeOAuth(t)
	other := e.createProvider("other", otherOAuth, nil)
	e.patchServerReq(b, map[string]any{"providerId": other.Id, "scopes": []string{"read:user"}})
	if e.grantCount(b.Id.String()) != "0" || e.grantCount(e.server.Id.String()) != "1" {
		t.Fatalf("grants after a provider change: B %s, A %s", e.grantCount(b.Id.String()), e.grantCount(e.server.Id.String()))
	}
	if v := e.scalar(`SELECT metadata->>'reason' FROM audit_log WHERE action = 'mcp_server.provider_grants_reset' AND target_id = $1`, b.Id.String()); v != "provider_changed" {
		t.Fatalf("reset reason = %q", v)
	}
	e.setServerRequirement(b, "repo")
	e.consentURL(e.callTool(e.key, "second__provider_token"), "server_not_allowed")
	e.patchServerReq(b, map[string]any{"providerId": e.provider.Id, "scopes": []string{"repo"}})

	// A removed requirement also ends it.
	e.allow(e.consentURL(e.callTool(e.key, "second__provider_token"), "server_not_allowed"))
	e.patchServerReq(b, nil)
	if e.grantCount(b.Id.String()) != "0" {
		t.Fatal("removing the requirement kept the grant")
	}
	e.setServerRequirement(b, "repo")
	e.allow(e.consentURL(e.callTool(e.key, "second__provider_token"), "server_not_allowed"))

	// Deleting the server deletes its grants (audited), not the other server's.
	code, ec := e.owner.call("DELETE", toolsTeam+"/mcp-servers/"+b.Id.String(), nil, nil, nil)
	mustCode(t, "delete server", code, ec, 200, "")
	if v := e.scalar(`SELECT count(*)::text FROM provider_server_grants`); v != "1" {
		t.Fatalf("grants after the delete = %s", v)
	}
	if v := e.scalar(`SELECT metadata->>'reason' || ' ' || (metadata->>'grantsDeleted') FROM audit_log
		WHERE action = 'mcp_server.provider_grants_reset' AND target_id = $1 ORDER BY id DESC LIMIT 1`, b.Id.String()); v != "server_deleted 1" {
		t.Fatalf("delete audit = %q", v)
	}

	// Disconnecting deletes the grants with the connection; reconnecting asks again.
	var conns []apitypes.ProviderConnection
	e.member.get("/v1/me/connections", &conns)
	code, ec = e.member.call("DELETE", "/v1/me/connections/"+conns[0].Id.String(), nil, nil, nil)
	mustCode(t, "disconnect", code, ec, 200, "")
	if v := e.scalar(`SELECT count(*)::text FROM provider_server_grants`); v != "0" {
		t.Fatalf("grants after a disconnect = %s", v)
	}
	e.consentURL(e.callTool(e.key, f3Tool), "not_connected")

	// A provider's delete and a person's delete take their connections' grants along.
	alex := e.scalar(`INSERT INTO users (oidc_issuer, oidc_subject, email, display_name) VALUES ('f3', 'f3', 'f3@example.com', 'F3') RETURNING id::text`)
	grantFor := func(providerID string) {
		e.scalar(`WITH c AS (INSERT INTO provider_connections (user_id, provider_id, status, scopes, access_token_cipher)
			VALUES ($1, $2, 'active', '{repo}', '\x00') RETURNING id)
			INSERT INTO provider_server_grants (connection_id, server_id, origin, scopes)
			SELECT id, $3, 'http://x.example', '{repo}' FROM c RETURNING 'x'`, alex, providerID, e.server.Id.String())
	}
	grantFor(other.Id.String())
	code, ec = e.admin.call("DELETE", providersBase+"/"+other.Id.String(), nil, nil, nil)
	mustCode(t, "delete provider", code, ec, 200, "")
	if v := e.scalar(`SELECT count(*)::text FROM provider_server_grants`); v != "0" {
		t.Fatalf("grants after a provider delete = %s", v)
	}
	grantFor(e.provider.Id.String())
	if _, err := e.app.Pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, alex); err != nil {
		t.Fatalf("delete user: %v", err)
	}
	if v := e.scalar(`SELECT count(*)::text FROM provider_server_grants`); v != "0" {
		t.Fatalf("grants after a user delete = %s", v)
	}
}

// The connections list says which servers each account may be used by, and
// the admin view of a person's connections does too (no token either way).
func TestConnectionsListAllowedServers(t *testing.T) {
	e := newProvEnv(t)
	e.connectBroadly("repo")
	var conns []apitypes.ProviderConnection
	if code := e.member.get("/v1/me/connections", &conns); code != 200 || len(conns) != 1 || conns[0].AllowedServers == nil || len(conns[0].AllowedServers) != 0 {
		t.Fatalf("a connection nobody may use = %d %+v", code, conns)
	}
	e.allow(e.consentURL(e.callTool(e.key, f3Tool), "server_not_allowed"))
	var admin []apitypes.ProviderConnection
	if code := e.admin.get("/v1/admin/users/"+e.userID("blair@localhost")+"/connections", &admin); code != 200 || len(admin) != 1 ||
		len(admin[0].AllowedServers) != 1 || admin[0].AllowedServers[0].Server.Slug != "status" {
		t.Fatalf("admin view = %d %+v", code, admin)
	}
	raw, _ := json.Marshal(admin)
	for _, sec := range e.oauth.Issued() {
		if strings.Contains(string(raw), sec) {
			t.Fatal("a token in the admin list")
		}
	}
}
