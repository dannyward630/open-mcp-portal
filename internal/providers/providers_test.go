package providers

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/ncecere/open-mcp-gateway/internal/apperr"
	"github.com/ncecere/open-mcp-gateway/internal/netpolicy"
	"github.com/ncecere/open-mcp-gateway/internal/secrets"
	"github.com/ncecere/open-mcp-gateway/internal/store/dbgen"
	"github.com/ncecere/open-mcp-gateway/internal/upstream"
)

type staticPolicy struct{ p *netpolicy.Policy }

func (s staticPolicy) Current(context.Context) *netpolicy.Policy { return s.p }

func testService(t *testing.T, up *upstream.Client) *Service {
	t.Helper()
	box, err := secrets.New([]byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	return New(nil, box, up, "https://mcp.example.com", nil)
}

func baseFields() fields {
	secret := "s3cret-value"
	return fields{name: "GitHub", clientID: "c", method: AuthClientSecretPost, enabled: true, pkce: true, secret: &secret,
		authorizationURL: "https://github.com/login/oauth/authorize", tokenURL: "https://github.com/login/oauth/access_token",
		scopes: []string{"repo"}, scopeParam: "scope", separator: " ", format: FormatStandard, params: map[string]string{}}
}

func reasonOf(err error) (code, field, reason string) {
	e, ok := apperr.As(err)
	if !ok {
		return "", "", ""
	}
	d, _ := e.Details.(map[string]any)
	field, _ = d["field"].(string)
	reason, _ = d["reason"].(string)
	return e.Code, field, reason
}

// Provider endpoints are checked like MCP server URLs (no development
// allowance here): https, public addresses, internal ones only with an
// allowlist entry, loopback and metadata never.
func TestValidateEndpoints(t *testing.T) {
	ctx := context.Background()
	s := testService(t, &upstream.Client{})
	ptr := func(v string) *string { return &v }
	cases := []struct {
		name               string
		change             func(f *fields)
		code, field, reasn string
	}{
		{"ok", func(*fields) {}, "", "", ""},
		{"http token", func(f *fields) { f.tokenURL = "http://github.com/token" }, "url_not_allowed", "tokenUrl", "scheme"},
		{"private token", func(f *fields) { f.tokenURL = "https://10.1.2.3/token" }, "url_not_allowed", "tokenUrl", "private_address"},
		{"loopback revocation", func(f *fields) { f.revocationURL = ptr("https://127.0.0.1/revoke") }, "url_not_allowed", "revocationUrl", "blocked_address"},
		{"metadata userinfo", func(f *fields) { f.userinfoURL = ptr("https://169.254.169.254/user") }, "url_not_allowed", "userinfoUrl", "blocked_address"},
		{"localhost authorize", func(f *fields) { f.authorizationURL = "https://localhost/authorize" }, "url_not_allowed", "authorizationUrl", "blocked_address"},
		{"user info", func(f *fields) { f.tokenURL = "https://u:p@github.com/token" }, "url_not_allowed", "tokenUrl", "userinfo"},
		{"fragment", func(f *fields) { f.tokenURL = "https://github.com/token#x" }, "url_not_allowed", "tokenUrl", "fragment"},
		{"public without pkce", func(f *fields) { f.method, f.pkce, f.secret = AuthNone, false, nil }, "pkce_required", "", ""},
		{"public with pkce", func(f *fields) { f.method, f.secret = AuthNone, nil }, "", "", ""},
		{"no secret", func(f *fields) { f.secret = nil }, "client_secret_required", "", ""},
		{"reserved param", func(f *fields) { f.params = map[string]string{"redirect_uri": "x"} }, "invalid_param", "", "reserved"},
		{"scope param as extra", func(f *fields) { f.scopeParam, f.params = "user_scope", map[string]string{"user_scope": "x"} }, "invalid_param", "", "reserved"},
		{"bad param key", func(f *fields) { f.params = map[string]string{"a b": "x"} }, "invalid_param", "", "invalid"},
		{"reserved scope param", func(f *fields) { f.scopeParam = "state" }, "invalid_scope_param", "", ""},
		{"bad scope", func(f *fields) { f.scopes = []string{`a"b`} }, "invalid_scope", "", ""},
		{"logo path", func(f *fields) { f.logoURL = ptr("/logos/gh.svg") }, "", "", ""},
		{"logo http", func(f *fields) { f.logoURL = ptr("http://x.example/l.svg") }, "url_not_allowed", "logoUrl", "scheme"},
		{"logo protocol-relative", func(f *fields) { f.logoURL = ptr("//evil.example/l.svg") }, "url_not_allowed", "logoUrl", "scheme"},
	}
	for _, c := range cases {
		f := baseFields()
		c.change(&f)
		code, field, reason := reasonOf(s.validate(ctx, &f))
		if code != c.code || field != c.field || reason != c.reasn {
			t.Errorf("%s: got %q %q %q, want %q %q %q", c.name, code, field, reason, c.code, c.field, c.reasn)
		}
	}
	// An allowlisted internal network with plain http.
	entries, err := netpolicy.ParseList("http:10.1.0.0/16")
	if err != nil {
		t.Fatal(err)
	}
	s = testService(t, &upstream.Client{Networks: staticPolicy{netpolicy.New(entries)}})
	f := baseFields()
	f.tokenURL = "http://10.1.2.3/token"
	if err := s.validate(ctx, &f); err != nil {
		t.Fatalf("allowlisted: %v", err)
	}
}

// The token endpoint is dialled through the guard: a host resolving to a
// blocked address is never contacted.
func TestEndpointGuard(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit = true }))
	defer srv.Close()
	up := &upstream.Client{
		Lookup: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("169.254.169.254")}, nil
		},
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
		},
	}
	s := testService(t, up)
	_, ee := s.exchange(context.Background(), dbgen.AuthProvider{TokenUrl: "https://tokens.example.com/token", TokenAuthMethod: AuthNone}, "code", "")
	if ee == nil || !ee.Unavailable || ee.Code != "not_allowed" || hit {
		t.Fatalf("guard: %+v hit=%v", ee, hit)
	}
}

func TestParseToken(t *testing.T) {
	cases := []struct {
		name, format, mt, body string
		status                 int
		access, scopes, code   string
		unavailable            bool
	}{
		{"json", FormatStandard, "application/json", `{"access_token":"a","refresh_token":"r","expires_in":"3600","scope":"x y"}`, 200, "a", "x y", "", false},
		{"form github", FormatStandard, "application/x-www-form-urlencoded", "access_token=a&scope=repo%2Cgist&token_type=bearer", 200, "a", "repo gist", "", false},
		{"error in 200", FormatStandard, "application/json", `{"error":"bad_verification_code","error_description":"secret stuff"}`, 200, "", "", "bad_verification_code", false},
		{"invalid_grant", FormatStandard, "application/json", `{"error":"invalid_grant"}`, 400, "", "", "invalid_grant", false},
		{"401 no body", FormatStandard, "text/plain", `nope`, 401, "", "", "http_401", false},
		{"5xx", FormatStandard, "text/html", `<html>`, 503, "", "", "server_error", true},
		{"429", FormatStandard, "application/json", `{"error":"slow"}`, 429, "", "", "server_error", true},
		{"no token", FormatStandard, "application/json", `{"token_type":"bearer"}`, 200, "", "", "invalid_response", true},
		{"slack", FormatSlackAuthedUser, "application/json", `{"ok":true,"access_token":"xoxb","authed_user":{"access_token":"xoxp","scope":"a,b"}}`, 200, "xoxp", "a b", "", false},
		{"slack not ok", FormatSlackAuthedUser, "application/json", `{"ok":false,"error":"invalid_code"}`, 200, "", "", "invalid_code", false},
	}
	for _, c := range cases {
		tok, ee := parseToken(c.format, c.status, c.mt, []byte(c.body))
		code, unav := "", false
		if ee != nil {
			code, unav = ee.Code, ee.Unavailable
		}
		if tok.Access != c.access || strings.Join(tok.Scopes, " ") != c.scopes || code != c.code || unav != c.unavailable {
			t.Errorf("%s: got %+v %+v", c.name, tok, ee)
		}
		if ee != nil && strings.Contains(ee.Error(), "secret stuff") {
			t.Errorf("%s: error carries the provider's description", c.name)
		}
	}
}

func TestSafeReturnPath(t *testing.T) {
	for p, ok := range map[string]bool{
		"/settings/connections": true, "/connect/done?x=1": true, "/": true,
		"": false, "//evil.example": false, "/\\evil.example": false, "https://evil.example": false, "javascript:alert(1)": false,
		"relative": false, "/a//b": false, "/a\nb": false, "/" + strings.Repeat("a", 2000): false,
	} {
		if SafeReturnPath(p) != ok {
			t.Errorf("SafeReturnPath(%q) = %v", p, !ok)
		}
	}
}
