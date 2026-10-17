package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

const (
	testIssuer   = "https://mcp.example.com"
	testAudience = "https://mail.example.com/mcp"
)

// fakeGateway signs identity tokens like the gateway and publishes its keys.
type fakeGateway struct {
	t       *testing.T
	mu      sync.Mutex
	keys    map[string]*ecdsa.PrivateKey
	publish []string // kids in the JWKS
	fetches atomic.Int32
	jwks    *httptest.Server
}

func newFakeGateway(t *testing.T) *fakeGateway {
	g := &fakeGateway{t: t, keys: map[string]*ecdsa.PrivateKey{}}
	g.addKey("kid-1", true)
	g.jwks = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		g.fetches.Add(1)
		g.mu.Lock()
		defer g.mu.Unlock()
		var set jose.JSONWebKeySet
		for _, kid := range g.publish {
			set.Keys = append(set.Keys, jose.JSONWebKey{Key: &g.keys[kid].PublicKey, KeyID: kid, Algorithm: "ES256", Use: "sig"})
		}
		_ = json.NewEncoder(w).Encode(set)
	}))
	t.Cleanup(g.jwks.Close)
	return g
}

func (g *fakeGateway) addKey(kid string, publish bool) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		g.t.Fatal(err)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.keys[kid] = k
	if publish {
		g.publish = append(g.publish, kid)
	}
}

func (g *fakeGateway) publishKey(kid string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.publish = append(g.publish, kid)
}

func (g *fakeGateway) jwksURL() string { return g.jwks.URL }

// claims are a valid user token's claims for testAudience; change them per
// test.
func baseClaims() map[string]any {
	now := time.Now()
	return map[string]any{
		"iss": testIssuer, "aud": testAudience, "sub": "6f1c2a40-0000-4000-8000-000000000001", "principal_type": "user",
		"email": "pat@example.com", "name": "Pat", "teams": []string{"support"}, "gateway": "mail", "tool": "mail__search_messages",
		"provider": "google", "jti": "jti-1", "iat": now.Unix(), "nbf": now.Add(-30 * time.Second).Unix(), "exp": now.Add(5 * time.Minute).Unix(),
	}
}

// claimsAt are baseClaims issued at now.
func claimsAt(now time.Time) map[string]any {
	return with(with(with(baseClaims(), "iat", now.Unix()), "nbf", now.Add(-30*time.Second).Unix()), "exp", now.Add(5*time.Minute).Unix())
}

func (g *fakeGateway) mint(kid string, claims map[string]any) string {
	g.mu.Lock()
	key := g.keys[kid]
	g.mu.Unlock()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader(jose.HeaderKey("kid"), kid))
	if err != nil {
		g.t.Fatal(err)
	}
	tok, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		g.t.Fatal(err)
	}
	return tok
}

func (g *fakeGateway) verifier() *Verifier {
	return &Verifier{JWKSURL: g.jwksURL(), Issuer: testIssuer, Audience: testAudience}
}

func with(c map[string]any, k string, v any) map[string]any {
	if v == nil {
		delete(c, k)
	} else {
		c[k] = v
	}
	return c
}

func TestVerifyAcceptsGatewayToken(t *testing.T) {
	g := newFakeGateway(t)
	id, err := g.verifier().Verify(context.Background(), g.mint("kid-1", baseClaims()))
	if err != nil {
		t.Fatal(err)
	}
	if id.Subject != "6f1c2a40-0000-4000-8000-000000000001" || id.Email != "pat@example.com" || id.Provider != "google" ||
		id.PrincipalType != "user" || id.ID != "jti-1" || id.Tool != "mail__search_messages" {
		t.Fatalf("identity = %+v", id)
	}
	// An audience list containing this server is fine too.
	if _, err := g.verifier().Verify(context.Background(), g.mint("kid-1", with(baseClaims(), "aud", []string{"x", testAudience}))); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyRejects(t *testing.T) {
	g := newFakeGateway(t)
	g.addKey("kid-unpublished", false)
	now := time.Now()
	hs, _ := jose.NewSigner(jose.SigningKey{Algorithm: jose.HS256, Key: []byte("0123456789abcdef0123456789abcdef")},
		(&jose.SignerOptions{}).WithHeader(jose.HeaderKey("kid"), "kid-1"))
	hsTok, _ := jwt.Signed(hs).Claims(baseClaims()).Serialize()
	parts := strings.Split(g.mint("kid-1", baseClaims()), ".")
	forged := g.mint("kid-1", with(baseClaims(), "sub", "someone-else"))
	tampered := parts[0] + "." + strings.Split(forged, ".")[1] + "." + parts[2]

	cases := []struct {
		name  string
		token string
		want  error
	}{
		{"empty", "", errNoToken},
		{"garbage", "not-a-jwt", errMalformed},
		{"HS256", hsTok, errMalformed},
		{"bad audience", g.mint("kid-1", with(baseClaims(), "aud", "https://other.example.com/mcp")), errClaims},
		{"bad issuer", g.mint("kid-1", with(baseClaims(), "iss", "https://evil.example.com")), errClaims},
		{"expired", g.mint("kid-1", with(with(baseClaims(), "exp", now.Add(-time.Minute).Unix()), "iat", now.Add(-6*time.Minute).Unix())), errClaims},
		{"not yet valid", g.mint("kid-1", with(baseClaims(), "nbf", now.Add(5*time.Minute).Unix())), errClaims},
		{"no exp", g.mint("kid-1", with(baseClaims(), "exp", nil)), errClaims},
		{"too long-lived", g.mint("kid-1", with(baseClaims(), "exp", now.Add(24*time.Hour).Unix())), errClaims},
		{"no principal_type", g.mint("kid-1", with(baseClaims(), "principal_type", nil)), errClaims},
		{"unknown kid", g.mint("kid-unpublished", baseClaims()), errUnknownKey},
		{"tampered payload", tampered, errSignature},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := g.verifier().Verify(context.Background(), c.token)
			if !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
		})
	}
}

// A token signed by a key that wasn't published when the set was cached is
// accepted once the gateway publishes it (fetch again for an unknown kid),
// and unknown kids don't trigger more than one fetch per MinRefetch.
func TestVerifyRefetchesForUnknownKid(t *testing.T) {
	g := newFakeGateway(t)
	clock := time.Now()
	v := g.verifier()
	v.Now = func() time.Time { return clock }
	ctx := context.Background()
	if _, err := v.Verify(ctx, g.mint("kid-1", baseClaims())); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Verify(ctx, g.mint("kid-1", baseClaims())); err != nil || g.fetches.Load() != 1 {
		t.Fatalf("cached verify: err=%v fetches=%d", err, g.fetches.Load())
	}

	g.addKey("kid-2", false)
	clock = clock.Add(11 * time.Second)
	if _, err := v.Verify(ctx, g.mint("kid-2", baseClaims())); !errors.Is(err, errUnknownKey) || g.fetches.Load() != 2 {
		t.Fatalf("unpublished kid: err=%v fetches=%d", err, g.fetches.Load())
	}
	// Within MinRefetch: no new fetch, still unknown.
	g.publishKey("kid-2")
	if _, err := v.Verify(ctx, g.mint("kid-2", baseClaims())); !errors.Is(err, errUnknownKey) || g.fetches.Load() != 2 {
		t.Fatalf("throttled: err=%v fetches=%d", err, g.fetches.Load())
	}
	clock = clock.Add(11 * time.Second)
	if _, err := v.Verify(ctx, g.mint("kid-2", baseClaims())); err != nil || g.fetches.Load() != 3 {
		t.Fatalf("rotated key: err=%v fetches=%d", err, g.fetches.Load())
	}
	// The cache expires after CacheTTL.
	clock = clock.Add(6 * time.Minute)
	if _, err := v.Verify(ctx, g.mint("kid-1", claimsAt(clock))); err != nil || g.fetches.Load() != 4 {
		t.Fatalf("stale cache: err=%v fetches=%d", err, g.fetches.Load())
	}
}

// A JWKS outage doesn't break tokens signed by a key already cached.
func TestVerifyKeepsCachedKeysWhenJWKSDown(t *testing.T) {
	g := newFakeGateway(t)
	clock := time.Now()
	v := g.verifier()
	v.Now = func() time.Time { return clock }
	if _, err := v.Verify(context.Background(), g.mint("kid-1", baseClaims())); err != nil {
		t.Fatal(err)
	}
	g.jwks.Close()
	clock = clock.Add(10 * time.Minute)
	if _, err := v.Verify(context.Background(), g.mint("kid-1", claimsAt(clock))); err != nil {
		t.Fatalf("cached key during outage: %v", err)
	}
}

func post(t *testing.T, h http.Handler, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestGuard(t *testing.T) {
	g := newFakeGateway(t)
	var seen *Identity
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen = IdentityFrom(r.Context()); w.WriteHeader(204) })
	guard := &Guard{Verifier: g.verifier()}
	h := guard.Wrap(next)
	list := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	call := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search_messages","arguments":{"query":""}}}`
	tok := g.mint("kid-1", baseClaims())

	cases := []struct {
		name    string
		body    string
		headers map[string]string
		want    int
	}{
		{"anonymous discovery", list, nil, 204},
		{"anonymous initialize", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`, nil, 204},
		{"anonymous call", call, nil, 401},
		{"anonymous batch with a call", "[" + list + "," + call + "]", nil, 401},
		{"anonymous with provider headers", list, map[string]string{headerProviderToken: "t"}, 401},
		{"bearer call", call, map[string]string{"Authorization": "Bearer " + tok, headerProvider: "google", headerProviderToken: "t"}, 204},
		{"identity header call", call, map[string]string{"Authorization": "Static abc", headerIdentity: tok, headerProvider: "google", headerProviderToken: "t"}, 204},
		{"invalid token on discovery", list, map[string]string{"Authorization": "Bearer " + tok + "x"}, 401},
		{"provider header without claim", call, map[string]string{"Authorization": "Bearer " + g.mint("kid-1", with(baseClaims(), "provider", nil)),
			headerProvider: "google", headerProviderToken: "t"}, 401},
		{"provider header mismatch", call, map[string]string{"Authorization": "Bearer " + tok, headerProvider: "github", headerProviderToken: "t"}, 401},
		{"token without provider header", call, map[string]string{"Authorization": "Bearer " + tok, headerProviderToken: "t"}, 401},
		{"no provider headers at all", call, map[string]string{"Authorization": "Bearer " + tok}, 204},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			seen = nil
			rec := post(t, h, c.body, c.headers)
			if rec.Code != c.want {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, c.want, rec.Body)
			}
			if c.want == 401 && !strings.Contains(rec.Header().Get("WWW-Authenticate"), "invalid_token") {
				t.Fatalf("WWW-Authenticate = %q", rec.Header().Get("WWW-Authenticate"))
			}
			if c.want == 204 && strings.Contains(c.name, "call") && (seen == nil || seen.Subject == "") {
				t.Fatalf("identity not passed on: %+v", seen)
			}
		})
	}

	// With a discovery key, anonymous discovery needs it.
	guard.DiscoveryHeader, guard.DiscoveryKey = "X-Discovery-Key", "k-0123456789"
	if rec := post(t, h, list, nil); rec.Code != 401 {
		t.Fatalf("discovery without key = %d", rec.Code)
	}
	if rec := post(t, h, list, map[string]string{"X-Discovery-Key": "k-0123456789"}); rec.Code != 204 {
		t.Fatalf("discovery with key = %d", rec.Code)
	}
	if rec := post(t, h, call, map[string]string{"X-Discovery-Key": "k-0123456789"}); rec.Code != 401 {
		t.Fatalf("call with only the key = %d", rec.Code)
	}
}
