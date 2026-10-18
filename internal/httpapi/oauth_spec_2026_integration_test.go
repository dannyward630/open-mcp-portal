package httpapi_test

import (
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"
)

// MCP 2026-07-28 authorization (docs/mcp-protocol.md "Authorization"):
// application_type in dynamic registration, and the two clients that matter
// (pi registers itself, Claude Code publishes a metadata document) still
// signing in and calling a gateway in the modern era.

func TestOAuthRegistrationApplicationType(t *testing.T) {
	e := newOAuthEnv(t)
	app := e.app
	e.fake.SetJWKS(app.URL+"/.well-known/jwks.json", e.fake.URL())

	// What pi sends: no application_type (it predates the requirement).
	// Nothing about it is invented in the answer.
	status, out, _ := jsonPost(t, app, "/oauth/register", map[string]any{"client_name": "pi", "redirect_uris": []string{testRedirect, "http://localhost:4567/callback"},
		"grant_types": []string{"authorization_code", "refresh_token"}, "response_types": []string{"code"}, "token_endpoint_auth_method": "none"})
	piID, _ := out["client_id"].(string)
	if status != 201 || !strings.HasPrefix(piID, "ompcl_") || out["application_type"] != nil || out["token_endpoint_auth_method"] != "none" {
		t.Fatalf("pi registers = %d %v", status, out)
	}

	cases := []struct{ name, applicationType string }{{"native", "native"}, {"web", "web"}}
	ids := map[string]string{"pi": piID}
	for _, c := range cases {
		status, out, _ := jsonPost(t, app, "/oauth/register", map[string]any{"client_name": "A " + c.name + " app", "redirect_uris": []string{testRedirect},
			"application_type": c.applicationType})
		id, _ := out["client_id"].(string)
		if status != 201 || out["application_type"] != c.applicationType || !strings.HasPrefix(id, "ompcl_") {
			t.Fatalf("%s registers = %d %v", c.name, status, out)
		}
		ids[c.name] = id
	}
	for _, bad := range []any{"mobile", "WEB", " native", 5, true, []string{"web"}} {
		status, out, _ := jsonPost(t, app, "/oauth/register", map[string]any{"redirect_uris": []string{testRedirect}, "application_type": bad})
		wantOAuthError(t, fmt.Sprintf("application_type %v", bad), status, out, 400, "invalid_client_metadata")
	}
	if n := e.scalar(`SELECT count(*)::text FROM audit_log WHERE action = 'oauth.client_register' AND after_state->>'applicationType' = 'native'`); n != "1" {
		t.Errorf("audit of the native registration = %s", n)
	}
	if n := e.scalar(`SELECT count(*)::text FROM audit_log WHERE action = 'oauth.client_register' AND after_state ? 'applicationType'`); n != "2" {
		t.Errorf("registrations with an application type in the audit = %s", n)
	}

	// Each of them signs in (iss in the redirect, resource in the token
	// request) and calls the gateway in both eras with its access token.
	for name, id := range ids {
		t.Run(name, func(t *testing.T) {
			access, refresh := connectApp(t, app, e.member, id, "support")
			status, out, _ := tokenCall(t, app, "/oauth/token", url.Values{"grant_type": {"refresh_token"}, "client_id": {id}, "refresh_token": {refresh},
				"resource": {app.URL + "/mcp/support"}})
			newAccess, _ := out["access_token"].(string)
			if status != 200 || newAccess == "" {
				t.Fatalf("refresh with resource = %d %v", status, out)
			}
			for _, token := range []string{access, newAccess} {
				code, _, got := rawModern(t, app, "support", token, "tools/list", nil, nil, nil)
				if code != 200 || got["error"] != nil || resultOf(t, got)["cacheScope"] != "private" ||
					!slices.Equal(namesOf(t, resultOf(t, got), "tools", "name"), []string{"status__whoami"}) {
					t.Fatalf("modern tools/list with an OAuth token = %d %v", code, got)
				}
			}
			code, _, got := rawModern(t, app, "support", newAccess, "tools/call", map[string]any{"name": "status__whoami"}, nil, nil)
			if code != 200 || got["error"] != nil || resultOf(t, got)["resultType"] != "complete" {
				t.Fatalf("modern call with an OAuth token = %d %v", code, got)
			}
			if code, _, got := rawMCP(t, app, "POST", "support", newAccess, rpc(1, "tools/list", nil), nil); code != 200 || got["error"] != nil {
				t.Fatalf("legacy tools/list with an OAuth token = %d %v", code, got)
			}
		})
	}
}

// Claude Code publishes a Client ID Metadata Document; its fields beyond
// the ones the portal reads (here application_type) change nothing.
func TestOAuthMetadataDocumentClientInModernEra(t *testing.T) {
	e := newOAuthEnv(t)
	app := e.app
	e.fake.SetJWKS(app.URL+"/.well-known/jwks.json", e.fake.URL())
	id := e.docs.URL + "/claude-code.json"
	e.docs.set("/claude-code.json", docResponse{status: 200, contentType: "application/json", body: `{"client_id":"` + id + `","client_name":"Claude Code",
		"client_uri":"https://claude.ai/code","redirect_uris":["http://localhost/callback","http://127.0.0.1/callback"],
		"grant_types":["authorization_code","refresh_token"],"response_types":["code"],"token_endpoint_auth_method":"none",
		"application_type":"native","scope":"ignored"}`})

	access, _ := connectApp(t, app, e.member, id, "support")
	code, _, got := rawModern(t, app, "support", access, "server/discover", nil, nil, nil)
	if code != 200 || got["error"] != nil || serverInfoOf(t, resultOf(t, got))["name"] != "support" {
		t.Fatalf("discover = %d %v", code, got)
	}
	// An error comes back to the client with iss too (RFC 9207), and the
	// metadata says so.
	q := authQuery(app, id, "support", newPKCE(), map[string]string{"response_type": "token"})
	res, _ := authorize(t, app, e.member, q)
	loc, _ := url.Parse(res.Header.Get("Location"))
	if res.StatusCode != 302 || loc.Query().Get("error") != "unsupported_response_type" || loc.Query().Get("iss") != app.URL || loc.Query().Get("state") != "st-1" {
		t.Fatalf("error redirect = %d %s", res.StatusCode, loc)
	}
	_, _, raw := getRaw(t, app.URL+"/.well-known/oauth-authorization-server")
	for _, want := range []string{`"authorization_response_iss_parameter_supported":true`, `"client_id_metadata_document_supported":true`,
		`"code_challenge_methods_supported":["S256"]`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("authorization server metadata lacks %s", want)
		}
	}
	// The resource indicator is required: without one, no code.
	q = authQuery(app, id, "support", newPKCE(), map[string]string{"resource": ""})
	res, _ = authorize(t, app, e.member, q)
	loc, _ = url.Parse(res.Header.Get("Location"))
	if loc.Query().Get("error") != "invalid_target" || loc.Query().Get("iss") != app.URL {
		t.Fatalf("no resource = %d %s", res.StatusCode, loc)
	}
}
