package httpapi_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ncecere/open-mcp-portal/internal/httpapi/apitypes"
	"github.com/ncecere/open-mcp-portal/internal/testutil"
)

// F-1 (docs/threat-model.md): a team editor can't read a server's stored
// static header value, so a URL edit that moves the server to another host
// must not send that value there. The value follows only a URL of the same
// origin; another origin needs a new value in the same request.

func (e *registryEnv) patchServer(s *session, id string, body map[string]any) (int, string, []byte, apitypes.McpServer) {
	e.t.Helper()
	cur := e.server(s, id)
	var out apitypes.McpServer
	code, raw := s.raw("PATCH", toolsTeam+"/mcp-servers/"+id, body, ifMatch(cur.Revision))
	if code == 200 {
		out = e.server(s, id)
	}
	return code, errorCode(raw), raw, out
}

// serverIDOf is the server's ID in a create response.
func serverIDOf(t *testing.T, raw []byte) string {
	t.Helper()
	var out struct{ Data apitypes.McpServerCreated }
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out.Data.Server.Id.String()
}

func (e *registryEnv) scalar(sql string, args ...any) string {
	e.t.Helper()
	var out string
	if err := e.app.Pool.QueryRow(context.Background(), sql, args...).Scan(&out); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	return out
}

func TestStaticHeaderValueDoesNotFollowOriginChange(t *testing.T) {
	e := newRegistryEnv(t)
	const first, second = "k-1234567890", "n-0987650001"
	e.fake.RequireHeader("X-API-Key", first)
	id := e.create(map[string]any{"slug": "status", "name": "Status", "url": e.fake.URL(), "authHeaderName": "X-API-Key", "authValue": first}).Server.Id.String()
	cipher := func() string {
		return e.scalar(`SELECT encode(auth_value_cipher, 'hex') FROM mcp_servers WHERE id = $1`, id)
	}
	before, cipherBefore := e.server(e.editor, id), cipher()
	audits := e.scalar(`SELECT count(*)::text FROM audit_log WHERE action = 'mcp_server.update'`)

	// The editor re-points the server at a host they control that wants the same header: refused, and nothing
	// reaches that host or changes.
	evil := testutil.NewFakeMCP(t)
	evil.RequireHeader("X-API-Key", first)
	code, ec, raw, _ := e.patchServer(e.editor, id, map[string]any{"url": evil.URL()})
	if code != 422 || ec != "auth_value_required" || errorDetails(raw)["field"] != "authValue" {
		t.Fatalf("url only = %d %s", code, raw)
	}
	if strings.Contains(string(raw), first) {
		t.Fatalf("the error leaks the value: %s", raw)
	}
	after := e.server(e.editor, id)
	if evil.Requests() != 0 || after.Url != before.Url || after.Revision != before.Revision || !after.HasAuth || after.AuthValueHint != "7890" ||
		cipher() != cipherBefore || e.scalar(`SELECT count(*)::text FROM audit_log WHERE action = 'mcp_server.update'`) != audits {
		t.Fatalf("a refused update changed something: %d requests, %+v", evil.Requests(), after)
	}
	// Other fields in the same request don't help, nor does resending the header's name or an unrelated change.
	for _, body := range []map[string]any{
		{"url": evil.URL(), "name": "Other"},
		{"url": evil.URL(), "authHeaderName": "X-API-Key"},
		{"url": evil.URL(), "authMode": "static_header"},
		{"url": evil.URL(), "enabled": true, "timeoutSeconds": 5},
	} {
		if code, ec, raw, _ := e.patchServer(e.editor, id, body); code != 422 || ec != "auth_value_required" {
			t.Fatalf("%v = %d %s", body, code, raw)
		}
	}
	if evil.Requests() != 0 || cipher() != cipherBefore {
		t.Fatalf("host got %d requests", evil.Requests())
	}
	// An empty or invalid new value is no new value.
	if code, ec, raw, _ := e.patchServer(e.editor, id, map[string]any{"url": evil.URL(), "authValue": ""}); code != 400 || ec != "invalid_auth_value" {
		t.Fatalf("empty value = %d %s", code, raw)
	}
	if evil.Requests() != 0 || cipher() != cipherBefore {
		t.Fatal("an invalid value moved the server")
	}

	// With a new value in the same request the move works, the old value is gone and the new host sees only the
	// new one.
	evil.RequireHeader("X-API-Key", second)
	code, ec, raw, moved := e.patchServer(e.editor, id, map[string]any{"url": evil.URL(), "authValue": second})
	if code != 200 {
		t.Fatalf("url and value = %d %s", code, raw)
	}
	if moved.Url != evil.URL() || !moved.HasAuth || moved.AuthValueHint != "0001" || moved.RefreshedAt == nil || moved.LastRefreshError != nil ||
		cipher() == cipherBefore || evil.Requests() == 0 {
		t.Fatalf("moved = %+v (%s)", moved, ec)
	}
	if got := e.scalar(`SELECT metadata->>'authValueChanged' || ' ' || (metadata->>'oldOrigin') || ' ' || (metadata->>'newOrigin')
		FROM audit_log WHERE action = 'mcp_server.update' ORDER BY id DESC LIMIT 1`); got != "true "+originOfURL(e.fake.URL())+" "+originOfURL(evil.URL()) {
		t.Fatalf("audit metadata = %q", got)
	}

	// Removing the header while moving is fine: nothing is sent.
	plain := testutil.NewFakeMCP(t)
	id2 := e.create(map[string]any{"slug": "other", "name": "Other", "url": e.fake.URL(), "authHeaderName": "X-API-Key", "authValue": first}).Server.Id.String()
	code, _, raw, moved = e.patchServer(e.editor, id2, map[string]any{"url": plain.URL(), "authHeaderName": ""})
	if code != 200 || moved.HasAuth || moved.Url != plain.URL() {
		t.Fatalf("move with the header removed = %d %s", code, raw)
	}
	// So is authMode none, which removes the header with its value.
	id3 := e.create(map[string]any{"slug": "third", "name": "Third", "url": e.fake.URL(), "authHeaderName": "X-API-Key", "authValue": first}).Server.Id.String()
	code, _, raw, moved = e.patchServer(e.editor, id3, map[string]any{"url": plain.URL(), "authMode": "none"})
	if code != 200 || moved.HasAuth {
		t.Fatalf("move with authMode none = %d %s", code, raw)
	}
	// A server without a header moves freely.
	id4 := e.create(map[string]any{"slug": "open", "name": "Open", "url": e.fake.URL()}).Server.Id.String()
	if code, _, raw, _ := e.patchServer(e.editor, id4, map[string]any{"url": plain.URL()}); code != 200 {
		t.Fatalf("move without a header = %d %s", code, raw)
	}
}

// A same-origin URL edit keeps the value and stays frictionless: the path, a
// trailing slash.
func TestStaticHeaderValueSurvivesSameOriginEdit(t *testing.T) {
	e := newRegistryEnv(t)
	const value = "k-1234567890"
	e.fake.RequireHeader("X-API-Key", value)
	id := e.create(map[string]any{"slug": "status", "name": "Status", "url": e.fake.URL(), "authHeaderName": "X-API-Key", "authValue": value}).Server.Id.String()
	cipher := e.scalar(`SELECT encode(auth_value_cipher, 'hex') FROM mcp_servers WHERE id = $1`, id)

	code, _, raw, got := e.patchServer(e.editor, id, map[string]any{"url": e.fake.URL() + "/"})
	if code != 200 || got.Url != e.fake.URL()+"/" || !got.HasAuth || got.AuthValueHint != "7890" || got.RefreshedAt == nil || got.LastRefreshError != nil ||
		e.scalar(`SELECT encode(auth_value_cipher, 'hex') FROM mcp_servers WHERE id = $1`, id) != cipher {
		t.Fatalf("trailing slash = %d %s %+v", code, raw, got)
	}
	if got := e.scalar(`SELECT coalesce(metadata->>'oldOrigin', 'none') FROM audit_log WHERE action = 'mcp_server.update' ORDER BY id DESC LIMIT 1`); got != "none" {
		t.Fatalf("a same-origin edit audited an origin change: %s", got)
	}
	// Back, with other settings and an explicit new value for the same host.
	if code, _, raw, got = e.patchServer(e.editor, id, map[string]any{"url": e.fake.URL(), "name": "Renamed"}); code != 200 || !got.HasAuth {
		t.Fatalf("back = %d %s", code, raw)
	}
}

// Under self_publish an editor's URL change doesn't go back to review, so the
// audit entry is the record: it names the old and new origin.
func TestURLChangeAuditedWithOriginsUnderSelfPublish(t *testing.T) {
	e := newRegistryEnv(t)
	base := toolsTeam + "/mcp-servers"
	var tp apitypes.TeamPublishing
	e.admin.get("/v1/admin/teams/tools/publishing", &tp)
	code, ec := e.admin.call("PUT", "/v1/admin/teams/tools/publishing", map[string]any{"override": "self_publish"}, nil, ifMatch(tp.Revision))
	mustCode(t, "self_publish", code, ec, 200, "")
	id := e.create(map[string]any{"slug": "status", "name": "Status", "url": e.fake.URL()}).Server.Id.String()
	code, ec = e.owner.call("POST", base+"/"+id+"/publish", nil, nil, nil)
	mustCode(t, "publish", code, ec, 200, "")

	moved := testutil.NewFakeMCP(t)
	code, ec, raw, sv := e.patchServer(e.editor, id, map[string]any{"url": moved.URL()})
	if code != 200 || sv.PublishState != "published" {
		t.Fatalf("move = %d %s %s", code, ec, raw)
	}
	var ap apitypes.AuditPage
	if code := e.admin.get("/v1/admin/audit?action=mcp_server.update", &ap); code != 200 || len(ap.Items) == 0 {
		t.Fatalf("audit = %d %+v", code, ap)
	}
	last := e.scalar(`SELECT metadata->>'oldOrigin' || ' -> ' || (metadata->>'newOrigin') || ' ' || (before_state->>'url') || ' -> ' || (after_state->>'url')
		FROM audit_log WHERE action = 'mcp_server.update' ORDER BY id DESC LIMIT 1`)
	if want := originOfURL(e.fake.URL()) + " -> " + originOfURL(moved.URL()) + " " + e.fake.URL() + " -> " + moved.URL(); last != want {
		t.Fatalf("audit = %q, want %q", last, want)
	}
}

// originOfURL is scheme://host:port of a test server's URL.
func originOfURL(u string) string { return u[:strings.Index(u, "/mcp")] }

// Under approval_required a published server with a delegated-provider
// requirement goes back to review when it moves to another origin, and the
// per-person tokens are not the editor's to redirect (F-3 covers those).
func TestOriginChangeOfRequirementServerGoesBackToReview(t *testing.T) {
	e := newProvEnv(t)
	e.publish(e.owner, e.server)
	other := testutil.NewFakeMCP(t)
	var cur, out apitypes.McpServer
	e.owner.get(e.serverPath(), &cur)
	code, ec := e.editor.call("PATCH", e.serverPath(), map[string]any{"url": other.URL()}, &out, ifMatch(cur.Revision))
	mustCode(t, "move", code, ec, 200, "")
	if out.PublishState != "pending" || out.Requirement == nil {
		t.Fatalf("after the move = %s %+v", out.PublishState, out.Requirement)
	}
	if got := e.scalar(`SELECT metadata->>'reason' FROM audit_log WHERE action = 'mcp_server.publish_request' ORDER BY id DESC LIMIT 1`); got != "url_changed" {
		t.Fatalf("review reason = %q", got)
	}
}

// In oauth mode a move to another origin erases every connection's tokens
// (the setup connection included) and sends the server back to the setup
// sign-in, exactly as a client change does.
func TestOAuthOriginChangeResetsConnections(t *testing.T) {
	e := newUpEnv(t, true)
	code, raw := e.createOAuth(e.editor, "gh", e.pm.URL(), map[string]any{"registration": "auto", "scopes": []string{"repo"}}, nil)
	if code != 201 {
		t.Fatalf("create = %d %s", code, raw)
	}
	id := serverIDOf(t, raw)
	if _, to := e.setup(e.editor, id); !strings.Contains(to, "setup=connected") {
		t.Fatalf("setup = %s", to)
	}
	sv := e.get(e.editor, id)
	if sv.Oauth.SetupConnection.Status != "active" || sv.RefreshedAt == nil || e.scalar(
		`SELECT count(*)::text FROM provider_connections pc JOIN auth_providers p ON p.id = pc.provider_id WHERE p.server_id = $1 AND pc.status = 'active'`, id) != "1" {
		t.Fatalf("before = %+v", sv.Oauth)
	}
	oldRequests := e.pm.Requests()

	// Another origin: a second protected server.
	pm2 := e.protectedFake(t)
	code, ec, moved := e.patch(e.editor, id, map[string]any{"url": pm2.URL()})
	mustCode(t, "move", code, ec, 200, "")
	if moved.Oauth == nil || moved.Oauth.SetupConnection.Status != "needs_reconsent" || moved.RefreshedAt != nil || moved.Oauth.Resource != pm2.URL() {
		t.Fatalf("after the move = %+v refreshedAt=%v", moved.Oauth, moved.RefreshedAt)
	}
	if n := e.scalar(`SELECT count(*)::text FROM provider_connections pc JOIN auth_providers p ON p.id = pc.provider_id
		WHERE p.server_id = $1 AND pc.status = 'needs_reconsent' AND pc.access_token_cipher IS NULL AND pc.refresh_token_cipher IS NULL`, id); n != "1" {
		t.Fatalf("connections with erased tokens = %s", n)
	}
	if e.pm.Requests() != oldRequests || len(pm2.ToolCalls()) != 0 {
		t.Fatalf("requests: old %d -> %d, calls at the new host %d", oldRequests, e.pm.Requests(), len(pm2.ToolCalls()))
	}
	if got := e.scalar(`SELECT (metadata->>'connectionsReset') || ' ' || (metadata->>'oldOrigin') || ' ' || (metadata->>'newOrigin')
		FROM audit_log WHERE action = 'mcp_server.auth_update' AND metadata ? 'oldOrigin' ORDER BY id DESC LIMIT 1`); got != "1 "+originOfURL(e.pm.URL())+" "+originOfURL(pm2.URL()) {
		t.Fatalf("auth_update audit = %q", got)
	}
	// The editor signs in again for the new resource: capabilities are read from it.
	if _, to := e.setup(e.editor, id); !strings.Contains(to, "setup=connected") {
		t.Fatalf("setup at the new origin = %s", to)
	}
	if sv = e.get(e.editor, id); sv.Oauth.SetupConnection.Status != "active" || sv.RefreshedAt == nil {
		t.Fatalf("after the new setup = %+v", sv.Oauth)
	}
}
