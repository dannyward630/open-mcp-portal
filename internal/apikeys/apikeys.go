// Package apikeys manages personal API keys (docs/decisions.md, decision
// 15): the credential MCP clients send to /mcp/{slug} in Phase 3.
//
// A key is "omcp_" followed by 43 random URL-safe characters (32 bytes).
// Only its HMAC-SHA256 digest, keyed with API_KEY_PEPPER, is stored, with
// its first characters (the prefix) so people can recognise it. The key is
// shown once, when it is created. Keys always expire, are limited to
// MaxActive per person, and authenticate nothing but /mcp/{slug}: the
// management API takes browser sessions only, so a key can't create keys.
package apikeys

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ncecere/open-mcp-gateway/internal/apperr"
	"github.com/ncecere/open-mcp-gateway/internal/audit"
	"github.com/ncecere/open-mcp-gateway/internal/authz"
	"github.com/ncecere/open-mcp-gateway/internal/httpx"
	"github.com/ncecere/open-mcp-gateway/internal/store"
	"github.com/ncecere/open-mcp-gateway/internal/store/dbgen"
)

// Prefix starts every key.
const Prefix = "omcp_"

// Limits.
const (
	// MaxActive is how many active (unrevoked, unexpired) keys a person may
	// have.
	MaxActive = 20
	// DefaultDays and MaxDays bound a key's lifetime.
	DefaultDays = 90
	MaxDays     = 365
	// prefixLen is how much of a key is stored in the clear: "omcp_" and 8.
	prefixLen = len(Prefix) + 8
	// secretLen is the length of a key: "omcp_" and 43 characters.
	secretLen = len(Prefix) + 43
)

// Statuses.
const (
	StatusActive  = "active"
	StatusExpired = "expired"
	StatusRevoked = "revoked"
)

var (
	errNotFound  = apperr.NotFound("api_key_not_found", "API key not found")
	errTooMany   = apperr.Conflict("too_many_keys", "You have 20 active API keys. Revoke one you no longer use first.")
	errBadName   = apperr.Invalid("invalid_name", "Name must be 1-100 characters on one line")
	errBadExpiry = apperr.Invalid("invalid_expiry", "expiresInDays must be between 1 and 365")
	// ErrInvalid is any key that doesn't authenticate: unknown, revoked,
	// expired, or its owner suspended.
	ErrInvalid = errors.New("invalid API key")
)

// Service manages keys.
type Service struct {
	pool   *pgxpool.Pool
	q      *dbgen.Queries
	pepper []byte
}

// New returns a Service; pepper is API_KEY_PEPPER (decoded).
func New(pool *pgxpool.Pool, pepper []byte) *Service {
	var q *dbgen.Queries
	if pool != nil {
		q = dbgen.New(pool)
	}
	return &Service{pool: pool, q: q, pepper: pepper}
}

// Digest is the stored form of a key: HMAC-SHA256 keyed with the pepper.
func Digest(pepper []byte, secret string) string {
	m := hmac.New(sha256.New, pepper)
	m.Write([]byte(secret))
	return hex.EncodeToString(m.Sum(nil))
}

// Status says whether a key is active, expired or revoked at now.
func Status(k dbgen.ApiKey, now time.Time) string {
	switch {
	case k.RevokedAt != nil:
		return StatusRevoked
	case !k.ExpiresAt.After(now):
		return StatusExpired
	}
	return StatusActive
}

// LooksLikeKey reports whether s has the shape of a key (before any lookup).
func LooksLikeKey(s string) bool {
	if len(s) != secretLen || !strings.HasPrefix(s, Prefix) {
		return false
	}
	for _, r := range s[len(Prefix):] {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// Created is a new key and its secret (shown once).
type Created struct {
	Key    dbgen.ApiKey
	Secret string
}

// snapshot is a key's audited form: never the secret or its digest.
func snapshot(k dbgen.ApiKey) map[string]any {
	return map[string]any{"name": k.Name, "prefix": k.Prefix, "expiresAt": k.ExpiresAt}
}

// Create makes a key for the actor (at most MaxActive active ones; days 0
// is DefaultDays). Audited as api_key.create, without the secret.
func (s *Service) Create(ctx context.Context, a authz.Actor, name string, days int32) (Created, error) {
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n < 1 || n > 100 || strings.ContainsAny(name, "\r\n\x00") {
		return Created{}, errBadName
	}
	if days == 0 {
		days = DefaultDays
	}
	if days < 1 || days > MaxDays {
		return Created{}, errBadExpiry
	}
	secret := Prefix + httpx.NewID(32)
	var out Created
	err := store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		// Locking the person serialises their creates, so the limit holds.
		if _, err := q.GetUserForUpdate(ctx, a.UserID); err != nil {
			return err
		}
		n, err := q.CountActiveAPIKeys(ctx, a.UserID)
		if err != nil {
			return err
		}
		if n >= MaxActive {
			return errTooMany
		}
		k, err := q.InsertAPIKey(ctx, dbgen.InsertAPIKeyParams{
			UserID: a.UserID, Name: name, Prefix: secret[:prefixLen], Digest: Digest(s.pepper, secret),
			ExpiresAt: time.Now().Add(time.Duration(days) * 24 * time.Hour),
		})
		if err != nil {
			return err
		}
		out = Created{Key: k, Secret: secret}
		e := a.Audit("api_key.create", "api_key", k.ID.String())
		e.After = snapshot(k)
		return audit.Record(ctx, q, e)
	})
	return out, err
}

// List returns the actor's keys, newest first: active ones, and those
// revoked or expired in the last 30 days.
func (s *Service) List(ctx context.Context, a authz.Actor) ([]dbgen.ApiKey, error) {
	return s.q.ListAPIKeys(ctx, a.UserID)
}

// Revoke revokes one of the actor's keys; it stops working at once.
// Revoking a revoked key changes nothing. Audited as api_key.revoke.
func (s *Service) Revoke(ctx context.Context, a authz.Actor, id uuid.UUID) error {
	return store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		cur, err := q.GetUserAPIKey(ctx, dbgen.GetUserAPIKeyParams{ID: id, UserID: a.UserID})
		if errors.Is(store.NotFound(err), store.ErrNotFound) {
			return errNotFound
		} else if err != nil {
			return err
		}
		if cur.RevokedAt != nil {
			return nil
		}
		k, err := q.RevokeAPIKey(ctx, dbgen.RevokeAPIKeyParams{ID: id, UserID: a.UserID})
		if errors.Is(store.NotFound(err), store.ErrNotFound) {
			return nil // revoked concurrently
		} else if err != nil {
			return err
		}
		e := a.Audit("api_key.revoke", "api_key", k.ID.String())
		e.Before = snapshot(cur)
		e.Metadata = map[string]any{"name": k.Name}
		return audit.Record(ctx, q, e)
	})
}

// Principal is who a key authenticates.
type Principal struct {
	Key  dbgen.ApiKey
	User dbgen.User
}

// Authenticate checks a key: it must exist, be unrevoked and unexpired, and
// belong to an active person. Every failure is ErrInvalid.
func (s *Service) Authenticate(ctx context.Context, secret string) (Principal, error) {
	if !LooksLikeKey(secret) {
		return Principal{}, ErrInvalid
	}
	row, err := s.q.GetAPIKeyByDigest(ctx, Digest(s.pepper, secret))
	if errors.Is(store.NotFound(err), store.ErrNotFound) {
		return Principal{}, ErrInvalid
	} else if err != nil {
		return Principal{}, err
	}
	// The digest lookup is exact; compare again in constant time anyway.
	if !hmac.Equal([]byte(row.ApiKey.Digest), []byte(Digest(s.pepper, secret))) ||
		Status(row.ApiKey, time.Now()) != StatusActive || row.User.Status != "active" {
		return Principal{}, ErrInvalid
	}
	return Principal{Key: row.ApiKey, User: row.User}, nil
}

// Touch records that a key was used (at most once a minute).
func (s *Service) Touch(ctx context.Context, id uuid.UUID) error { return s.q.TouchAPIKey(ctx, id) }
