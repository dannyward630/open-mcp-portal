// Package identity signs the identity the gateway sends to upstream MCP
// servers with every forwarded call (docs/decisions.md, decisions 7 and 19;
// docs/upstream-identity.md): a short-lived ES256 JWT naming the person, their
// teams, the gateway and the tool, for the server's URL as audience.
//
// Signing keys live in signing_keys: the private key sealed with
// ENCRYPTION_KEY (internal/secrets, bound to the kid), the public key as a
// JWK. A key is generated on first use. Every RotationPeriod a leader-elected
// loop (PostgreSQL advisory lock) replaces it; the old key keeps being
// published at /.well-known/jwks.json for PublishRetiring, far longer than
// any token it signed lives (TokenTTL), and is then retired.
package identity

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ncecere/open-mcp-gateway/internal/httpx"
	"github.com/ncecere/open-mcp-gateway/internal/secrets"
	"github.com/ncecere/open-mcp-gateway/internal/store"
	"github.com/ncecere/open-mcp-gateway/internal/store/dbgen"
)

// Timings.
const (
	// TokenTTL is how long an identity token is valid.
	TokenTTL = 5 * time.Minute
	// RotationPeriod is the age at which the signing key is replaced.
	RotationPeriod = 30 * 24 * time.Hour
	// PublishRetiring is how long a replaced key stays in the JWKS.
	PublishRetiring = 24 * time.Hour
	// RotationCheckInterval is how often the rotation loop looks.
	RotationCheckInterval = time.Hour
	// cacheFor is how long a replica signs with the key it loaded before
	// reading the active key again (a rotation on another replica takes
	// effect within this time; the old key is still published).
	cacheFor = time.Minute
)

// rotationLockKey is the advisory lock of the rotation loop. Creating and
// rotating keys also takes the transaction lock omg.signing-keys
// (LockSigningKeys), which is a different key so the loop can't block on
// itself.
const rotationLockKey = "omg.signing-key-rotation"

// Headers that carry the token (decision 19).
const (
	HeaderAuthorization = "Authorization"
	HeaderIdentity      = "MCP-Gateway-Identity"
)

// Header is where a server receives the token: Authorization (as a Bearer
// token) unless the server's own static header is Authorization, in which
// case MCP-Gateway-Identity (the bare token). It returns the header name and
// its value.
func Header(staticHeader, token string) (name, value string) {
	if strings.EqualFold(staticHeader, HeaderAuthorization) {
		return HeaderIdentity, token
	}
	return HeaderAuthorization, "Bearer " + token
}

// Claims are what a token says beyond iss, iat, exp and jti.
type Claims struct {
	Audience string   // the server's URL
	Subject  string   // the person's user ID
	Email    string   //
	Name     string   //
	Teams    []string // slugs of the person's teams
	Gateway  string   // the gateway's slug
	Tool     string   // the exposed tool name (tools/call)
	Prompt   string   // the exposed prompt name (prompts/get)
	Client   string   // clientInfo.name when known, else the API key's name
}

// tokenClaims are the private claims of a token.
type tokenClaims struct {
	Email   string   `json:"email,omitempty"`
	Name    string   `json:"name,omitempty"`
	Teams   []string `json:"teams"`
	Gateway string   `json:"gateway"`
	Tool    string   `json:"tool,omitempty"`
	Prompt  string   `json:"prompt,omitempty"`
	Client  string   `json:"client,omitempty"`
}

// Service signs tokens and publishes keys.
type Service struct {
	pool   *pgxpool.Pool
	q      *dbgen.Queries
	box    *secrets.Box
	issuer string
	log    *slog.Logger
	now    func() time.Time

	mu       sync.Mutex
	cached   *signingKey
	loadedAt time.Time
}

type signingKey struct {
	kid    string
	signer jose.Signer
}

// New returns a Service; issuer is APP_URL.
func New(pool *pgxpool.Pool, box *secrets.Box, issuer string, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	var q *dbgen.Queries
	if pool != nil {
		q = dbgen.New(pool)
	}
	return &Service{pool: pool, q: q, box: box, issuer: issuer, log: log, now: time.Now}
}

// Issuer is the iss claim (APP_URL).
func (s *Service) Issuer() string { return s.issuer }

func keyAAD(kid string) []byte { return []byte("signing_key:" + kid) }

// Sign issues a token for c, valid for TokenTTL.
func (s *Service) Sign(ctx context.Context, c Claims) (string, error) {
	k, err := s.active(ctx)
	if err != nil {
		return "", err
	}
	now := s.now()
	reg := jwt.Claims{
		Issuer: s.issuer, Subject: c.Subject, Audience: jwt.Audience{c.Audience},
		IssuedAt: jwt.NewNumericDate(now), NotBefore: jwt.NewNumericDate(now.Add(-30 * time.Second)),
		Expiry: jwt.NewNumericDate(now.Add(TokenTTL)), ID: httpx.NewID(16),
	}
	teams := c.Teams
	if teams == nil {
		teams = []string{}
	}
	priv := tokenClaims{Email: c.Email, Name: c.Name, Teams: teams, Gateway: c.Gateway, Tool: c.Tool, Prompt: c.Prompt, Client: c.Client}
	return jwt.Signed(k.signer).Claims(reg).Claims(priv).Serialize()
}

// active returns the signing key, creating the first one if there is none.
func (s *Service) active(ctx context.Context) (*signingKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cached != nil && s.now().Sub(s.loadedAt) < cacheFor {
		return s.cached, nil
	}
	row, err := s.q.GetActiveSigningKey(ctx)
	if errors.Is(store.NotFound(err), store.ErrNotFound) {
		if err := s.ensureKey(ctx); err != nil {
			return nil, err
		}
		row, err = s.q.GetActiveSigningKey(ctx)
	}
	if err != nil {
		return nil, err
	}
	k, err := s.load(row)
	if err != nil {
		return nil, err
	}
	s.cached, s.loadedAt = k, s.now()
	return k, nil
}

func (s *Service) load(row dbgen.SigningKey) (*signingKey, error) {
	der, err := s.box.Open(row.PrivateKeyCipher, keyAAD(row.Kid))
	if err != nil {
		return nil, fmt.Errorf("signing key %s: %w", row.Kid, err)
	}
	priv, err := x509.ParseECPrivateKey(der)
	if err != nil {
		return nil, fmt.Errorf("signing key %s: %w", row.Kid, err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: priv},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader(jose.HeaderKey("kid"), row.Kid))
	if err != nil {
		return nil, err
	}
	return &signingKey{kid: row.Kid, signer: signer}, nil
}

// ensureKey creates the first signing key unless one exists.
func (s *Service) ensureKey(ctx context.Context) error {
	return store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		if err := q.LockSigningKeys(ctx); err != nil {
			return err
		}
		if _, err := q.GetActiveSigningKey(ctx); err == nil {
			return nil
		} else if !errors.Is(store.NotFound(err), store.ErrNotFound) {
			return err
		}
		kid, err := s.insertKey(ctx, q)
		if err == nil {
			s.log.InfoContext(ctx, "identity signing key created", "kid", kid)
		}
		return err
	})
}

// insertKey generates and stores a new active key.
func (s *Service) insertKey(ctx context.Context, q *dbgen.Queries) (string, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", err
	}
	pub := jose.JSONWebKey{Key: &priv.PublicKey, Algorithm: string(jose.ES256), Use: "sig"}
	thumb, err := pub.Thumbprint(crypto.SHA256)
	if err != nil {
		return "", err
	}
	kid := base64.RawURLEncoding.EncodeToString(thumb)
	pub.KeyID = kid
	jwk, err := json.Marshal(pub)
	if err != nil {
		return "", err
	}
	der, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return "", err
	}
	sealed, err := s.box.Seal(der, keyAAD(kid))
	if err != nil {
		return "", err
	}
	return kid, q.InsertSigningKey(ctx, dbgen.InsertSigningKeyParams{Kid: kid, PrivateKeyCipher: sealed, PublicJwk: jwk})
}

// JWKS returns the published keys: the active one and those retiring. The
// first call creates a key if there is none, so the set is never empty.
func (s *Service) JWKS(ctx context.Context) (jose.JSONWebKeySet, error) {
	if _, err := s.active(ctx); err != nil {
		return jose.JSONWebKeySet{}, err
	}
	rows, err := s.q.ListPublishedSigningKeys(ctx)
	if err != nil {
		return jose.JSONWebKeySet{}, err
	}
	set := jose.JSONWebKeySet{Keys: make([]jose.JSONWebKey, 0, len(rows))}
	for _, r := range rows {
		var k jose.JSONWebKey
		if err := json.Unmarshal(r.PublicJwk, &k); err != nil {
			return jose.JSONWebKeySet{}, fmt.Errorf("signing key %s: %w", r.Kid, err)
		}
		set.Keys = append(set.Keys, k)
	}
	return set, nil
}

// Rotation is what one rotation pass did.
type Rotation struct {
	Skipped bool   // another replica holds the lock
	Created string // the kid of a new key
	Retired int64  // keys no longer published
}

// Rotate runs one pass if no other replica is: it creates the first key,
// replaces a key older than RotationPeriod (the old one is published for
// PublishRetiring more), and retires keys whose publication ended.
func (s *Service) Rotate(ctx context.Context) (Rotation, error) {
	release, ok, err := store.TryAdvisoryLockText(ctx, s.pool, rotationLockKey)
	if err != nil {
		return Rotation{}, err
	}
	if !ok {
		return Rotation{Skipped: true}, nil
	}
	defer release()
	var out Rotation
	err = store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		if err := q.LockSigningKeys(ctx); err != nil {
			return err
		}
		var err error
		if out.Retired, err = q.RetireExpiredSigningKeys(ctx); err != nil {
			return err
		}
		cur, err := q.GetActiveSigningKey(ctx)
		switch {
		case errors.Is(store.NotFound(err), store.ErrNotFound):
		case err != nil:
			return err
		default:
			due, err := q.ActiveSigningKeyDue(ctx, int64(RotationPeriod/time.Second))
			if err != nil || !due {
				return err
			}
			if _, err := q.RetireActiveSigningKey(ctx, dbgen.RetireActiveSigningKeyParams{
				Kid: cur.Kid, PublishedSeconds: int64(PublishRetiring / time.Second),
			}); err != nil {
				return err
			}
		}
		out.Created, err = s.insertKey(ctx, q)
		return err
	})
	if err != nil {
		return Rotation{}, err
	}
	if out.Created != "" {
		s.mu.Lock()
		s.cached = nil
		s.mu.Unlock()
	}
	return out, nil
}

// RotationLoop runs Rotate at start and every RotationCheckInterval until
// ctx is cancelled.
func (s *Service) RotationLoop(ctx context.Context, log *slog.Logger) {
	t := time.NewTicker(RotationCheckInterval)
	defer t.Stop()
	for {
		r, err := s.Rotate(ctx)
		switch {
		case err != nil && ctx.Err() == nil:
			log.Warn("identity key rotation failed", "err", err)
		case r.Created != "" || r.Retired > 0:
			log.Info("identity signing keys rotated", "created", r.Created, "retired", r.Retired)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
