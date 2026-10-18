package httpapi_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/ncecere/open-mcp-portal/internal/httpapi/apitypes"
	"github.com/ncecere/open-mcp-portal/internal/registry"
	"github.com/ncecere/open-mcp-portal/internal/testutil"
)

// A manual Test sends the editor's identity token the way a call would
// (decision 61), and says so when a server refuses it (the DeepWiki finding).

// identityTestClaims are the claims a Test's token carries.
type identityTestClaims struct {
	PrincipalType string   `json:"principal_type"`
	Subject       string   `json:"sub"`
	Email         string   `json:"email"`
	Name          string   `json:"name"`
	Teams         []string `json:"teams"`
	Gateway       string   `json:"gateway"`
	Tool          string   `json:"tool"`
	Prompt        string   `json:"prompt"`
	Resource      string   `json:"resource"`
	Client        string   `json:"client"`
}

// bearers are the bare tokens of the requests that sent Authorization: Bearer.
func bearers(f *testutil.FakeMCP) []string {
	var out []string
	for _, a := range f.RequestAuths() {
		if tok, ok := strings.CutPrefix(a.Authorization, "Bearer "); ok {
			out = append(out, tok)
		}
	}
	return out
}

func (e *gwEnv) healthChecks(id string) string {
	e.t.Helper()
	return e.scalar(`SELECT coalesce(string_agg(trigger || ':' || ok || ':' || coalesce(error_class, '-') || ':' || coalesce(http_status::text, '-'), ',' ORDER BY checked_at, id), '')
		FROM mcp_health_checks WHERE server_id = $1`, id)
}

func TestManualTestSendsIdentityToken(t *testing.T) {
	logs := &syncBuffer{}
	e := newGwEnv(t, slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	jwks := e.app.URL + "/.well-known/jwks.json"
	alexID := e.userID("alex@localhost")
	var sentinels, bodies []string
	// testServer presses Test as s and keeps the response body.
	testServer := func(s *session, id string) (int, string, apitypes.McpTestResult) {
		t.Helper()
		code, raw := s.raw("POST", toolsTeam+"/mcp-servers/"+id+"/test", nil, nil)
		bodies = append(bodies, string(raw))
		var env struct{ Data apitypes.McpTestResult }
		if code == 200 {
			if err := json.Unmarshal(raw, &env); err != nil {
				t.Fatalf("decode: %v: %s", err, raw)
			}
		}
		return code, errorCode(raw), env.Data
	}

	// A server that accepts the token: the Test is an ordinary success, and
	// the token it sent is a valid identity token for the editor who pressed
	// the button, for the server's URL, naming no gateway and no tool.
	t.Run("accepted", func(t *testing.T) {
		f := testutil.NewFakeMCP(t)
		sv := e.createServer(e.owner, "tools", "accepts", f.URL(), nil)
		before := len(f.RequestAuths())
		code, ec, tr := testServer(e.editor, sv.Id.String())
		mustCode(t, "test", code, ec, 200, "")
		if !tr.Ok || tr.ErrorClass != nil {
			t.Fatalf("test = %+v", tr)
		}
		toks := bearers(f)
		if len(toks) == 0 {
			t.Fatal("the Test sent no identity token")
		}
		for _, a := range f.RequestAuths()[:before] {
			if a.Authorization != "" || a.Identity != "" {
				t.Fatalf("creating the server (a refresh) sent credentials: %+v", a)
			}
		}
		for _, tok := range toks {
			var c identityTestClaims
			if err := testutil.VerifyIdentity(context.Background(), tok, jwks, f.URL(), &c); err != nil {
				t.Fatalf("verify: %v", err)
			}
			if c.PrincipalType != "user" || c.Subject != alexID || c.Email != "alex@localhost" || c.Name == "" || len(c.Teams) != 1 || c.Teams[0] != "tools" ||
				c.Gateway != registry.TestGateway || c.Client != registry.TestClient || c.Tool != "" || c.Prompt != "" || c.Resource != "" {
				t.Fatalf("claims = %+v", c)
			}
		}
		if testutil.VerifyIdentity(context.Background(), toks[0], jwks, "https://elsewhere.example/mcp", nil) == nil {
			t.Fatal("the token verified for another audience")
		}
		if got := e.healthChecks(sv.Id.String()); !strings.HasSuffix(got, "manual:true:-:-") {
			t.Fatalf("checks = %s", got)
		}
		sentinels = append(sentinels, toks...)
	})

	// A server that refuses any Authorization header but works without it
	// (what DeepWiki's public endpoint does before a call): the Test fails
	// with the actionable message, recorded as the manual check. A refresh
	// still works, because it sends no token.
	t.Run("refused with the token only", func(t *testing.T) {
		f := testutil.NewFakeMCP(t)
		sv := e.createServer(e.owner, "tools", "refuses", f.URL(), nil)
		id := sv.Id.String()
		f.RejectAuthorization(http.StatusUnauthorized)
		code, ec, tr := testServer(e.editor, id)
		mustCode(t, "test", code, ec, 200, "")
		if tr.Ok || tr.ErrorClass == nil || *tr.ErrorClass != apitypes.McpErrorClassAuth || tr.HttpStatus == nil || *tr.HttpStatus != 401 ||
			tr.Message == nil || !strings.Contains(*tr.Message, "without its identity token but refused them with it") ||
			!strings.Contains(*tr.Message, "Turn off Send identity token") {
			t.Fatalf("test = %+v", tr)
		}
		if tr.Counts != nil {
			t.Fatalf("a failed test counted capabilities: %+v", tr.Counts)
		}
		if got := e.healthChecks(id); got != "manual:true:-:-,manual:false:auth:401" {
			t.Fatalf("checks = %s", got)
		}
		msg := e.scalar(`SELECT message FROM mcp_health_checks WHERE server_id = $1 AND NOT ok`, id)
		if !strings.Contains(msg, "Turn off Send identity token") {
			t.Fatalf("stored message = %q", msg)
		}
		// Exactly one request carried the token (the refused one); the second
		// probe sent none.
		toks := bearers(f)
		if len(toks) == 0 {
			t.Fatal("no token was sent")
		}
		last := f.RequestAuths()[len(f.RequestAuths())-1]
		if last.Authorization != "" || last.Identity != "" {
			t.Fatalf("the second probe carried credentials: %+v", last)
		}
		var refreshed apitypes.McpRefreshResult
		code, ec = e.editor.call("POST", toolsTeam+"/mcp-servers/"+id+"/refresh", nil, &refreshed, nil)
		mustCode(t, "refresh", code, ec, 200, "")
		sentinels = append(sentinels, toks...)
		// With Send identity off the same server passes, and nothing is sent.
		before := len(f.RequestAuths())
		var cur apitypes.McpServer
		e.editor.get(toolsTeam+"/mcp-servers/"+id, &cur)
		code, ec = e.editor.call("PATCH", toolsTeam+"/mcp-servers/"+id, map[string]any{"sendIdentity": false}, nil, ifMatch(cur.Revision))
		mustCode(t, "turn off", code, ec, 200, "")
		code, ec, tr = testServer(e.editor, id)
		mustCode(t, "test without identity", code, ec, 200, "")
		if !tr.Ok {
			t.Fatalf("test with Send identity off = %+v", tr)
		}
		for _, a := range f.RequestAuths()[before:] {
			if a.Authorization != "" || a.Identity != "" {
				t.Fatalf("Send identity is off but a request carried %+v", a)
			}
		}
	})

	// A server that fails with and without the token: the ordinary failure,
	// not the diagnosis.
	t.Run("refused both ways", func(t *testing.T) {
		f := testutil.NewFakeMCP(t)
		sv := e.createServer(e.owner, "tools", "refuses-all", f.URL(), nil)
		f.RequireHeader("X-Key", "needed")
		code, ec, tr := testServer(e.editor, sv.Id.String())
		mustCode(t, "test", code, ec, 200, "")
		if tr.Ok || tr.ErrorClass == nil || *tr.ErrorClass != apitypes.McpErrorClassAuth || tr.HttpStatus == nil || *tr.HttpStatus != 401 ||
			tr.Message == nil || strings.Contains(*tr.Message, "identity token") {
			t.Fatalf("test = %+v", tr)
		}
		sentinels = append(sentinels, bearers(f)...)
	})

	// A server with its own Authorization header: the identity token travels
	// in MCP-Gateway-Identity, the static value is sent unchanged.
	t.Run("static Authorization header", func(t *testing.T) {
		f := testutil.NewFakeMCP(t)
		f.RequireHeader("Authorization", "Bearer static-1234567890")
		sv := e.createServer(e.owner, "tools", "static-auth", f.URL(), map[string]any{"authHeaderName": "Authorization", "authValue": "Bearer static-1234567890"})
		code, ec, tr := testServer(e.editor, sv.Id.String())
		mustCode(t, "test", code, ec, 200, "")
		if !tr.Ok {
			t.Fatalf("test = %+v", tr)
		}
		auths := f.RequestAuths()
		last := auths[len(auths)-1]
		if last.Authorization != "Bearer static-1234567890" || last.Identity == "" {
			t.Fatalf("last request = %+v", last)
		}
		var c identityTestClaims
		if err := testutil.VerifyIdentity(context.Background(), last.Identity, jwks, f.URL(), &c); err != nil || c.Subject != alexID {
			t.Fatalf("verify: %v %+v", err, c)
		}
		sentinels = append(sentinels, last.Identity)
	})

	// Only editors test: a member is refused and an outsider doesn't see the
	// team; neither reached the server.
	t.Run("who may test", func(t *testing.T) {
		f := testutil.NewFakeMCP(t)
		sv := e.createServer(e.owner, "tools", "guarded", f.URL(), nil)
		before := len(f.RequestAuths())
		code, ec, _ := testServer(e.member, sv.Id.String())
		mustCode(t, "member", code, ec, 403, "forbidden")
		code, _, _ = testServer(e.outsider, sv.Id.String())
		if code != 404 {
			t.Fatalf("outsider = %d", code)
		}
		if n := len(f.RequestAuths()); n != before {
			t.Fatalf("a refused Test reached the server (%d requests)", n-before)
		}
	})

	// The token never shows in a response, the logs, the audit log, health
	// checks or the server rows.
	if len(sentinels) < 4 {
		t.Fatalf("only %d tokens were captured", len(sentinels))
	}
	var dump string
	if err := e.app.Pool.QueryRow(context.Background(), `SELECT
		(SELECT coalesce(string_agg(row_to_json(a)::text, E'\n'), '') FROM audit_log a) ||
		(SELECT coalesce(string_agg(row_to_json(h)::text, E'\n'), '') FROM mcp_health_checks h) ||
		(SELECT coalesce(string_agg(row_to_json(s)::text, E'\n'), '') FROM mcp_servers s) ||
		(SELECT coalesce(string_agg(row_to_json(n)::text, E'\n'), '') FROM notification_inbox n) ||
		(SELECT coalesce(string_agg(row_to_json(o)::text, E'\n'), '') FROM notification_outbox o)`).Scan(&dump); err != nil {
		t.Fatal(err)
	}
	for _, tok := range sentinels {
		if strings.Contains(dump, tok) || strings.Contains(logs.String(), tok) || strings.Contains(strings.Join(bodies, "\n"), tok) {
			t.Fatalf("an identity token (%.12s…) leaked into a response, the database or the logs", tok)
		}
	}
}
