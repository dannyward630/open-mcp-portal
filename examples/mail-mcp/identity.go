package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// Headers the gateway sends (docs/upstream-identity.md).
const (
	headerIdentity      = "MCP-Gateway-Identity"
	headerProvider      = "MCP-Gateway-Provider"
	headerProviderToken = "MCP-Gateway-Provider-Token"
)

// Identity is the part of the gateway's identity token this server uses.
type Identity struct {
	PrincipalType string   `json:"principal_type"` // "user" or "service_account"
	Email         string   `json:"email"`          // absent for service accounts
	Name          string   `json:"name"`
	Teams         []string `json:"teams"`
	Gateway       string   `json:"gateway"`
	Tool          string   `json:"tool"`
	Client        string   `json:"client"`
	// Provider is the slug of the auth provider whose user token came with
	// this call in MCP-Gateway-Provider-Token; empty when none did.
	Provider string `json:"provider"`

	// Filled from the registered claims after verification.
	Subject string    `json:"-"`
	ID      string    `json:"-"` // jti, handy in logs
	Expiry  time.Time `json:"-"`
}

// Verification failures. Their text is safe to log (never the token).
var (
	errNoToken    = errors.New("no identity token")
	errMalformed  = errors.New("identity token is not an ES256 JWT with one kid")
	errUnknownKey = errors.New("identity token kid is not in the gateway's JWKS")
	errSignature  = errors.New("identity token signature does not verify")
	errClaims     = errors.New("identity token claims are not acceptable")
)

// Verifier checks the gateway's identity tokens: ES256 signature by a key
// in the gateway's JWKS (cached, fetched again for an unknown kid), iss =
// Issuer, aud = Audience (this server's URL exactly as registered), and a
// current exp/nbf/iat with 30 s of leeway.
type Verifier struct {
	JWKSURL  string
	Issuer   string
	Audience string

	// HTTP fetches the JWKS (default: a client with a 10 s timeout).
	HTTP *http.Client
	// CacheTTL is how long a fetched key set is used (default 5 minutes,
	// the gateway's Cache-Control max-age).
	CacheTTL time.Duration
	// MinRefetch limits fetches triggered by unknown kids (default 10 s),
	// so a flood of forged tokens can't turn into a flood of JWKS requests.
	MinRefetch time.Duration
	// Now is the clock (tests).
	Now func() time.Time

	mu        sync.Mutex
	keys      jose.JSONWebKeySet
	fetchedAt time.Time
	triedAt   time.Time
}

func (v *Verifier) now() time.Time {
	if v.Now != nil {
		return v.Now()
	}
	return time.Now()
}

// TokenFromHeader finds the identity token: MCP-Gateway-Identity when the
// server registered a static Authorization header, else the bearer token.
func TokenFromHeader(h http.Header) string {
	if v := h.Get(headerIdentity); v != "" {
		return strings.TrimSpace(v)
	}
	if v := h.Get("Authorization"); len(v) > 7 && strings.EqualFold(v[:7], "Bearer ") {
		return strings.TrimSpace(v[7:])
	}
	return ""
}

// Verify checks raw and returns its identity.
func (v *Verifier) Verify(ctx context.Context, raw string) (*Identity, error) {
	if raw == "" {
		return nil, errNoToken
	}
	tok, err := jwt.ParseSigned(raw, []jose.SignatureAlgorithm{jose.ES256})
	if err != nil || len(tok.Headers) != 1 || tok.Headers[0].KeyID == "" {
		return nil, errMalformed
	}
	key, err := v.key(ctx, tok.Headers[0].KeyID)
	if err != nil {
		return nil, err
	}
	var std jwt.Claims
	var id Identity
	if err := tok.Claims(key.Key, &std, &id); err != nil {
		return nil, errSignature
	}
	now := v.now()
	if err := std.ValidateWithLeeway(jwt.Expected{Issuer: v.Issuer, AnyAudience: jwt.Audience{v.Audience}, Time: now}, 30*time.Second); err != nil {
		return nil, fmt.Errorf("%w: %v", errClaims, err)
	}
	// ValidateWithLeeway accepts a token without exp or iat; the gateway
	// always sets them (exp = iat + 5 min), so insist on them.
	switch {
	case std.Expiry == nil || std.IssuedAt == nil:
		return nil, fmt.Errorf("%w: exp and iat are required", errClaims)
	case std.Expiry.Time().Sub(std.IssuedAt.Time()) > 15*time.Minute:
		return nil, fmt.Errorf("%w: lifetime longer than 15 minutes", errClaims)
	case std.Subject == "" || (id.PrincipalType != "user" && id.PrincipalType != "service_account"):
		return nil, fmt.Errorf("%w: sub and principal_type are required", errClaims)
	}
	id.Subject, id.ID, id.Expiry = std.Subject, std.ID, std.Expiry.Time()
	return &id, nil
}

// key returns the public key for kid, fetching the JWKS when the cache is
// stale or doesn't know kid.
func (v *Verifier) key(ctx context.Context, kid string) (jose.JSONWebKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	ttl, minRefetch := v.CacheTTL, v.MinRefetch
	if ttl == 0 {
		ttl = 5 * time.Minute
	}
	if minRefetch == 0 {
		minRefetch = 10 * time.Second
	}
	now := v.now()
	cached := v.keys.Key(kid)
	fresh := !v.fetchedAt.IsZero() && now.Sub(v.fetchedAt) < ttl
	if len(cached) > 0 && fresh {
		return cached[0], nil
	}
	// Stale cache, or an unknown kid (a new signing key): fetch again, but
	// not more often than minRefetch.
	if v.triedAt.IsZero() || now.Sub(v.triedAt) >= minRefetch || !fresh {
		v.triedAt = now
		set, err := v.fetch(ctx)
		if err == nil {
			v.keys, v.fetchedAt = set, now
			cached = v.keys.Key(kid)
		} else if len(cached) == 0 {
			return jose.JSONWebKey{}, fmt.Errorf("%w (JWKS fetch failed: %v)", errUnknownKey, err)
		}
		// On a failed fetch keep using a cached key we already trust.
	}
	if len(cached) == 0 {
		return jose.JSONWebKey{}, errUnknownKey
	}
	return cached[0], nil
}

func (v *Verifier) fetch(ctx context.Context) (jose.JSONWebKeySet, error) {
	var set jose.JSONWebKeySet
	client := v.HTTP
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.JWKSURL, nil)
	if err != nil {
		return set, err
	}
	res, err := client.Do(req)
	if err != nil {
		return set, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return set, fmt.Errorf("HTTP %d", res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return set, err
	}
	if err := json.Unmarshal(body, &set); err != nil {
		return set, err
	}
	// Only public ES256 signing keys are acceptable.
	keys := set.Keys[:0]
	for _, k := range set.Keys {
		if k.IsPublic() && k.Valid() && (k.Algorithm == "" || k.Algorithm == string(jose.ES256)) && (k.Use == "" || k.Use == "sig") {
			keys = append(keys, k)
		}
	}
	set.Keys = keys
	return set, nil
}

// --- HTTP middleware ---

type identityKey struct{}

// IdentityFrom returns the verified identity a request carried (nil for an
// anonymous discovery request).
func IdentityFrom(ctx context.Context) *Identity {
	id, _ := ctx.Value(identityKey{}).(*Identity)
	return id
}

// discoveryMethods are the JSON-RPC methods the gateway sends without an
// identity token: discovery (refresh) and health checks only open a
// connection (server/discover for protocol 2026-07-28, else the initialize
// handshake) and list capabilities. Lists reveal only definitions, which the
// gateway shows its users anyway.
var discoveryMethods = map[string]bool{
	"server/discover": true, "initialize": true, "notifications/initialized": true, "ping": true,
	"tools/list": true, "prompts/list": true, "resources/list": true, "resources/templates/list": true,
}

// maxRequestBody bounds a JSON-RPC request.
const maxRequestBody = 1 << 20

// Guard is the authentication middleware in front of the MCP handler.
type Guard struct {
	Verifier *Verifier
	// DiscoveryHeader and DiscoveryKey, when set, make anonymous discovery
	// requests carry this static header (register it as the server's static
	// header in the gateway; the gateway sends it on every request).
	DiscoveryHeader, DiscoveryKey string
	// Logf logs one line per refused request (never a token).
	Logf func(format string, args ...any)
}

func (g *Guard) logf(format string, args ...any) {
	if g.Logf != nil {
		g.Logf(format, args...)
	}
}

func unauthorized(w http.ResponseWriter, desc string) {
	w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token", error_description="`+desc+`"`)
	http.Error(w, "unauthorized: "+desc, http.StatusUnauthorized)
}

// Wrap verifies the identity token of every request. A request with a token
// must verify; a request without one may only be discovery (initialize,
// ping, tools/list), everything else gets 401. Provider headers without a
// matching provider claim also get 401: they didn't come from the gateway.
func (g *Guard) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := TokenFromHeader(r.Header)
		if raw == "" {
			if g.DiscoveryKey != "" && subtle.ConstantTimeCompare([]byte(r.Header.Get(g.DiscoveryHeader)), []byte(g.DiscoveryKey)) != 1 {
				g.logf("refused %s %s: no identity token and no discovery key", r.Method, r.URL.Path)
				unauthorized(w, "identity token required")
				return
			}
			if r.Header.Get(headerProvider) != "" || r.Header.Get(headerProviderToken) != "" {
				g.logf("refused: provider headers without an identity token")
				unauthorized(w, "provider headers without identity token")
				return
			}
			methods, ok := peekMethods(w, r)
			if !ok {
				return
			}
			for _, m := range methods {
				if !discoveryMethods[m] {
					g.logf("refused %s without identity token", m)
					unauthorized(w, "identity token required")
					return
				}
			}
			next.ServeHTTP(w, r)
			return
		}
		id, err := g.Verifier.Verify(r.Context(), raw)
		if err != nil {
			g.logf("refused request: %v", err)
			unauthorized(w, "identity token rejected")
			return
		}
		if p := r.Header.Get(headerProvider); (p != "" || r.Header.Get(headerProviderToken) != "") && p != id.Provider {
			g.logf("refused request jti=%s: provider header %q does not match the provider claim %q", id.ID, p, id.Provider)
			unauthorized(w, "provider header does not match the identity token")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), identityKey{}, id)))
	})
}

// peekMethods reads the JSON-RPC method names of a POST body and puts the
// body back. It writes the error response itself when it returns false.
func peekMethods(w http.ResponseWriter, r *http.Request) ([]string, bool) {
	if r.Method != http.MethodPost {
		// Stateless servers answer GET and DELETE with 405 anyway; nothing
		// to authorize.
		return nil, true
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody+1))
	if err != nil {
		http.Error(w, "cannot read body", http.StatusBadRequest)
		return nil, false
	}
	if len(body) > maxRequestBody {
		http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
		return nil, false
	}
	r.Body = io.NopCloser(strings.NewReader(string(body)))
	type msg struct {
		Method string `json:"method"`
	}
	var one msg
	if err := json.Unmarshal(body, &one); err == nil {
		return []string{one.Method}, true
	}
	var batch []msg
	if err := json.Unmarshal(body, &batch); err == nil {
		out := make([]string, 0, len(batch))
		for _, m := range batch {
			out = append(out, m.Method)
		}
		return out, true
	}
	// Not JSON: let the SDK answer with its parse error.
	return nil, true
}
