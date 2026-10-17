package oauthmeta_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/ncecere/open-mcp-gateway/internal/testutil"
	"github.com/ncecere/open-mcp-gateway/internal/upstream"
	"github.com/ncecere/open-mcp-gateway/internal/upstream/oauthmeta"
)

func TestParseChallenges(t *testing.T) {
	cs := oauthmeta.ParseChallenges([]string{
		`Bearer error="invalid_token", scope="repo read:org", resource_metadata="https://api.example.com/.well-known/oauth-protected-resource/mcp/"`,
		`Basic realm="x", Bearer realm=y`,
	})
	if len(cs) != 3 || !cs[0].IsBearer() || cs[0].Error() != "invalid_token" || !slices.Equal(cs[0].Scopes(), []string{"repo", "read:org"}) ||
		cs[0].ResourceMetadata() != "https://api.example.com/.well-known/oauth-protected-resource/mcp/" {
		t.Fatalf("challenges = %+v", cs)
	}
	if cs[1].Scheme != "Basic" || cs[1].Params["realm"] != "x" || !cs[2].IsBearer() || cs[2].Params["realm"] != "y" {
		t.Fatalf("second header = %+v", cs[1:])
	}
	if b := oauthmeta.Bearer([]string{`Basic realm="a"`}); b != nil {
		t.Fatalf("bearer from basic = %+v", b)
	}
	if b := oauthmeta.Bearer([]string{`bearer`}); b == nil || b.Error() != "" {
		t.Fatalf("bare bearer = %+v", b)
	}
	if b := oauthmeta.Bearer([]string{`Bearer scope="a \"b\"", error=insufficient_scope`}); b == nil || b.Error() != "insufficient_scope" ||
		b.Params["scope"] != `a "b"` {
		t.Fatalf("escapes = %+v", b)
	}
}

func TestMetadataURLs(t *testing.T) {
	got := oauthmeta.AuthorizationServerMetadataURLs("https://github.com/login/oauth")
	want := []string{"https://github.com/.well-known/oauth-authorization-server/login/oauth",
		"https://github.com/.well-known/openid-configuration/login/oauth", "https://github.com/login/oauth/.well-known/openid-configuration"}
	if !slices.Equal(got, want) {
		t.Fatalf("path issuer = %v", got)
	}
	if got := oauthmeta.AuthorizationServerMetadataURLs("https://auth.example.com"); !slices.Equal(got,
		[]string{"https://auth.example.com/.well-known/oauth-authorization-server", "https://auth.example.com/.well-known/openid-configuration"}) {
		t.Fatalf("root issuer = %v", got)
	}
	if got := oauthmeta.WellKnownResourceURLs("https://api.githubcopilot.com/mcp/"); !slices.Equal(got,
		[]string{"https://api.githubcopilot.com/.well-known/oauth-protected-resource/mcp/", "https://api.githubcopilot.com/.well-known/oauth-protected-resource"}) {
		t.Fatalf("resource URLs = %v", got)
	}
	for _, c := range []struct {
		resource, url string
		exact, near   bool
	}{
		{"https://a.example/mcp", "https://a.example/mcp", true, true},
		{"https://a.example/mcp/", "https://a.example/mcp", false, true},
		{"https://A.example/mcp", "https://a.example/mcp", false, true},
		{"https://a.example/other", "https://a.example/mcp", false, false},
		{"https://evil.example/mcp", "https://a.example/mcp", false, false},
	} {
		if e, n := oauthmeta.ResourceMatches(c.resource, c.url); e != c.exact || n != c.near {
			t.Errorf("%s vs %s = %v %v", c.resource, c.url, e, n)
		}
	}
}

// protected starts an OAuth-protected fake MCP server and its fake
// authorization server.
func protected(t *testing.T, change func(o *testutil.FakeOAuth, p *testutil.Protection)) (*testutil.FakeMCP, *testutil.FakeOAuth) {
	t.Helper()
	o := testutil.NewFakeOAuth(t)
	o.Set(func(f *testutil.FakeOAuth) { f.ScopesSupported = []string{"repo", "write"} })
	p := testutil.Protection{Introspect: func(_ context.Context, tok string) testutil.Introspection { return o.Introspect(tok) },
		ScopesSupported: []string{"repo", "write"}}
	if change != nil {
		change(o, &p)
	}
	p.Issuer = o.Issuer()
	m := testutil.NewFakeMCP(t)
	m.Protect(p)
	return m, o
}

func fetcher() *oauthmeta.Fetcher {
	return &oauthmeta.Fetcher{Client: &upstream.Client{AllowPrivate: true}}
}

func codes(r oauthmeta.Result) []string {
	var out []string
	for _, p := range r.Problems {
		out = append(out, p.Code)
	}
	return out
}

func TestDiscover(t *testing.T) {
	ctx := context.Background()
	// GitHub's shape: issuer with a path, metadata only at the path-insertion
	// URL, iss in responses, no registration.
	m, o := protected(t, func(o *testutil.FakeOAuth, p *testutil.Protection) {
		o.Set(func(f *testutil.FakeOAuth) { f.IssuerPath, f.IssParam = "/login/oauth", true })
		p.ChallengeScope = "repo"
	})
	r := fetcher().Discover(ctx, m.URL(), oauthmeta.Options{})
	if !r.Usable() || r.Outcome != oauthmeta.OutcomeOAuth || r.Server.Issuer != o.Issuer() ||
		r.Server.MetadataURL != o.URL+"/.well-known/oauth-authorization-server/login/oauth" || !r.Server.IssParameterSupported ||
		r.Server.RegistrationEndpoint != "" || !slices.Equal(r.SuggestedScopes, []string{"repo"}) || r.FoundByFallback ||
		!slices.Contains(codes(r), oauthmeta.ProblemNoRegistration) {
		t.Fatalf("github-like = %+v %+v", r, r.Server)
	}
	if n := len(r.Steps); n != 3 || r.Steps[0].Result != oauthmeta.StepChallenge || r.Steps[1].Purpose != "resource_metadata" {
		t.Fatalf("steps = %+v", r.Steps)
	}
	// OIDC path appending, after two misses; DCR; scopes from the metadata.
	m, o = protected(t, func(o *testutil.FakeOAuth, _ *testutil.Protection) {
		o.Set(func(f *testutil.FakeOAuth) { f.IssuerPath, f.MetadataAt, f.DCR = "/tenant", "oidc-append", true })
	})
	r = fetcher().Discover(ctx, m.URL(), oauthmeta.Options{})
	if !r.Usable() || r.Server.MetadataURL != o.URL+"/tenant/.well-known/openid-configuration" || r.Server.RegistrationEndpoint != o.URL+"/register" ||
		!slices.Equal(r.SuggestedScopes, []string{"repo", "write"}) || len(r.Steps) != 5 {
		t.Fatalf("oidc append = %+v", r)
	}
	// The well-known fallback when the challenge has no resource_metadata.
	m, _ = protected(t, func(_ *testutil.FakeOAuth, p *testutil.Protection) { p.NoResourceMetadataParam = true })
	r = fetcher().Discover(ctx, m.URL(), oauthmeta.Options{})
	if !r.Usable() || !r.FoundByFallback || !slices.Contains(codes(r), oauthmeta.ProblemNoResourceMetadata) ||
		r.Resource.URL != strings.TrimSuffix(m.URL(), "/mcp")+"/.well-known/oauth-protected-resource/mcp" {
		t.Fatalf("fallback = %+v", r)
	}
	// Resource mismatch: near (a trailing slash) suggests the URL; other is blocking.
	m, _ = protected(t, func(_ *testutil.FakeOAuth, p *testutil.Protection) { p.IgnoreResource = true })
	m.SetProtection(func(p *testutil.Protection) { p.Resource = m.URL() + "/" })
	r = fetcher().Discover(ctx, m.URL(), oauthmeta.Options{})
	if r.Usable() || r.SuggestedURL != m.URL()+"/" || !slices.Contains(codes(r), oauthmeta.ProblemResourceMismatch) || r.Server == nil {
		t.Fatalf("near mismatch = %+v", r)
	}
	m.SetProtection(func(p *testutil.Protection) { p.Resource = "https://evil.example/mcp" })
	r = fetcher().Discover(ctx, m.URL(), oauthmeta.Options{})
	if r.Usable() || r.SuggestedURL != "" || r.Server != nil || !slices.Contains(codes(r), oauthmeta.ProblemResourceMismatch) {
		t.Fatalf("mismatch = %+v", r)
	}
	// Issuer mismatch and no PKCE are blocking.
	m, _ = protected(t, func(o *testutil.FakeOAuth, _ *testutil.Protection) {
		o.Set(func(f *testutil.FakeOAuth) { f.MetadataIssuer, f.NoPKCE = "https://other.example", true })
	})
	r = fetcher().Discover(ctx, m.URL(), oauthmeta.Options{})
	if r.Usable() || !slices.Contains(codes(r), oauthmeta.ProblemIssuerMismatch) || !slices.Contains(codes(r), oauthmeta.ProblemNoPKCE) {
		t.Fatalf("issuer/pkce = %+v", r)
	}
	// No metadata at all.
	m, _ = protected(t, func(o *testutil.FakeOAuth, _ *testutil.Protection) {
		o.Set(func(f *testutil.FakeOAuth) { f.MetadataAt = "none" })
	})
	r = fetcher().Discover(ctx, m.URL(), oauthmeta.Options{})
	if r.Usable() || !slices.Contains(codes(r), oauthmeta.ProblemMetadataNotFound) {
		t.Fatalf("no metadata = %+v", r)
	}
	// An unprotected server, and one that wants another scheme.
	plain := testutil.NewFakeMCP(t)
	if r := fetcher().Discover(ctx, plain.URL(), oauthmeta.Options{}); r.Outcome != oauthmeta.OutcomeNoAuth {
		t.Fatalf("plain = %+v", r)
	}
	plain.RequireHeader("X-Api-Key", "k")
	if r := fetcher().Discover(ctx, plain.URL(), oauthmeta.Options{}); r.Outcome != oauthmeta.OutcomeOtherAuth {
		t.Fatalf("other auth = %+v", r)
	}
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(502) }))
	defer down.Close()
	if r := fetcher().Discover(ctx, down.URL+"/mcp", oauthmeta.Options{}); r.Outcome != oauthmeta.OutcomeUnreachable {
		t.Fatalf("down = %+v", r)
	}
}

// The guard: loopback refused without the development allowance, redirects
// and oversized answers refused, at most 8 requests.
func TestDiscoverGuard(t *testing.T) {
	ctx := context.Background()
	m, _ := protected(t, nil)
	strict := &oauthmeta.Fetcher{Client: &upstream.Client{}}
	r := strict.Discover(ctx, m.URL(), oauthmeta.Options{})
	if r.Outcome != oauthmeta.OutcomeUnreachable || len(r.Steps) != 1 || r.Steps[0].Result != oauthmeta.StepNotAllowed {
		t.Fatalf("loopback without allowance = %+v", r)
	}
	// URLs from answers are guarded like the server's: a redirect (here to the
	// cloud metadata address) is never followed.
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/mcp":
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="http://`+r.Host+`/prm"`)
			w.WriteHeader(401)
		case "/prm":
			http.Redirect(w, r, "http://169.254.169.254/latest", http.StatusFound)
		}
	}))
	defer redirect.Close()
	r = fetcher().Discover(ctx, redirect.URL+"/mcp", oauthmeta.Options{})
	if r.Outcome != oauthmeta.OutcomeOtherAuth || r.Steps[1].Result != oauthmeta.StepNotAllowed {
		t.Fatalf("redirect = %+v", r.Steps)
	}
	huge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/mcp" {
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="http://`+r.Host+`/prm"`)
			w.WriteHeader(401)
			return
		}
		_, _ = w.Write([]byte(`{"resource":"` + strings.Repeat("a", 70<<10) + `"}`))
	}))
	defer huge.Close()
	r = fetcher().Discover(ctx, huge.URL+"/mcp", oauthmeta.Options{})
	if r.Outcome != oauthmeta.OutcomeOtherAuth || r.Steps[1].Result != oauthmeta.StepError {
		t.Fatalf("too large = %+v", r.Steps)
	}
	capped := &oauthmeta.Fetcher{Client: &upstream.Client{AllowPrivate: true}, MaxRequests: 2}
	r = capped.Discover(ctx, m.URL(), oauthmeta.Options{})
	if len(r.Steps) != 2 || r.Server != nil {
		t.Fatalf("cap = %+v", r)
	}
}

func TestRegisterAndProbe(t *testing.T) {
	ctx := context.Background()
	m, o := protected(t, func(o *testutil.FakeOAuth, _ *testutil.Protection) {
		o.Set(func(f *testutil.FakeOAuth) { f.DCR = true })
	})
	reg, err := fetcher().Register(ctx, o.URL+"/register", oauthmeta.RegistrationRequest{ClientName: "Gateway · Fake",
		RedirectURIs: []string{"http://127.0.0.1/connect/callback"}, GrantTypes: []string{"authorization_code"}, TokenEndpointAuthMethod: "client_secret_basic"})
	if err != nil || reg.ClientID == "" || reg.ClientSecret == "" || reg.TokenEndpointAuthMethod != "client_secret_basic" {
		t.Fatalf("register = %+v %v", reg, err)
	}
	o.Set(func(f *testutil.FakeOAuth) { f.DCR = false })
	if _, err := fetcher().Register(ctx, o.URL+"/register", oauthmeta.RegistrationRequest{RedirectURIs: []string{"x"}}); err == nil {
		t.Fatal("registration without DCR worked")
	}
	pr := fetcher().Probe(ctx, m.URL())
	if pr.Status != 401 || pr.Challenge == nil || pr.Challenge.ResourceMetadata() == "" {
		t.Fatalf("probe = %+v", pr)
	}
}
