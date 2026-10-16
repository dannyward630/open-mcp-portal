package identity_test

import (
	"context"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/ncecere/open-mcp-gateway/internal/identity"
	"github.com/ncecere/open-mcp-gateway/internal/secrets"
	"github.com/ncecere/open-mcp-gateway/internal/testutil"
)

func newService(t *testing.T) (*identity.Service, func(sql string)) {
	t.Helper()
	pool, _ := testutil.NewDB(t)
	key, _ := secrets.ParseKey("MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2Nzg5MDE=")
	box, err := secrets.New(key)
	if err != nil {
		t.Fatal(err)
	}
	exec := func(sql string) {
		t.Helper()
		if _, err := pool.Exec(context.Background(), sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	return identity.New(pool, box, "https://mcp.example.com", testutil.Logger()), exec
}

// verify checks a token against a key set: signature, issuer, audience and
// expiry.
func verify(set jose.JSONWebKeySet, token string) (jwt.Claims, error) {
	var c jwt.Claims
	tok, err := jwt.ParseSigned(token, []jose.SignatureAlgorithm{jose.ES256})
	if err != nil {
		return c, err
	}
	keys := set.Key(tok.Headers[0].KeyID)
	if len(keys) == 0 {
		return c, jose.ErrUnsupportedKeyType
	}
	if err := tok.Claims(keys[0].Key, &c); err != nil {
		return c, err
	}
	return c, c.Validate(jwt.Expected{Issuer: "https://mcp.example.com", AnyAudience: jwt.Audience{"https://srv.example.com/mcp"}, Time: time.Now()})
}

func kidOf(t *testing.T, token string) string {
	tok, err := jwt.ParseSigned(token, []jose.SignatureAlgorithm{jose.ES256})
	if err != nil {
		t.Fatal(err)
	}
	return tok.Headers[0].KeyID
}

func TestSigningAndRotation(t *testing.T) {
	s, exec := newService(t)
	ctx := context.Background()
	claims := identity.Claims{Audience: "https://srv.example.com/mcp", Subject: "u1", Gateway: "g", Tool: "s__t"}

	// The first key is created on first use.
	old, err := s.Sign(ctx, claims)
	if err != nil {
		t.Fatal(err)
	}
	set, err := s.JWKS(ctx)
	if err != nil || len(set.Keys) != 1 || set.Keys[0].Algorithm != "ES256" || !set.Keys[0].IsPublic() {
		t.Fatalf("jwks = %+v %v", set, err)
	}
	c, err := verify(set, old)
	if err != nil || c.Expiry.Time().Sub(c.IssuedAt.Time()) != identity.TokenTTL {
		t.Fatalf("verify = %+v %v", c, err)
	}
	// Nothing to rotate yet.
	if r, err := s.Rotate(ctx); err != nil || r.Created != "" || r.Retired != 0 {
		t.Fatalf("early rotation = %+v %v", r, err)
	}

	// After 30 days the key is replaced; the old one stays published, so
	// tokens it signed still verify.
	exec(`UPDATE signing_keys SET created_at = now() - interval '31 days'`)
	r, err := s.Rotate(ctx)
	if err != nil || r.Created == "" || r.Created == kidOf(t, old) {
		t.Fatalf("rotation = %+v %v", r, err)
	}
	fresh, err := s.Sign(ctx, claims)
	if err != nil || kidOf(t, fresh) != r.Created {
		t.Fatalf("signed with %s, want %s (%v)", kidOf(t, fresh), r.Created, err)
	}
	set, _ = s.JWKS(ctx)
	if len(set.Keys) != 2 {
		t.Fatalf("published keys = %d", len(set.Keys))
	}
	for _, tok := range []string{old, fresh} {
		if _, err := verify(set, tok); err != nil {
			t.Fatalf("token of %s: %v", kidOf(t, tok), err)
		}
	}

	// A day later the old key is retired and no longer published.
	exec(`UPDATE signing_keys SET retire_after = now() - interval '1 minute' WHERE state = 'retiring'`)
	if r, err := s.Rotate(ctx); err != nil || r.Retired != 1 || r.Created != "" {
		t.Fatalf("retire = %+v %v", r, err)
	}
	set, _ = s.JWKS(ctx)
	if len(set.Keys) != 1 || set.Keys[0].KeyID != kidOf(t, fresh) {
		t.Fatalf("after retirement = %+v", set)
	}
	if _, err := verify(set, old); err == nil {
		t.Fatal("a retired key's token still verifies")
	}
	exec(`DO $$ BEGIN IF EXISTS (SELECT 1 FROM signing_keys WHERE state = 'retired' AND private_key_cipher IS NOT NULL) THEN RAISE 'kept'; END IF; END $$`)
}

func TestHeaderChoice(t *testing.T) {
	for static, want := range map[string]string{"": "Authorization", "X-API-Key": "Authorization", "authorization": "MCP-Gateway-Identity"} {
		name, value := identity.Header(static, "tok")
		if name != want || (name == "Authorization") != (value == "Bearer tok") {
			t.Errorf("static %q: %s %s", static, name, value)
		}
	}
}
