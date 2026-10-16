package oauth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ncecere/open-mcp-gateway/internal/audit"
	"github.com/ncecere/open-mcp-gateway/internal/authz"
	"github.com/ncecere/open-mcp-gateway/internal/store"
	"github.com/ncecere/open-mcp-gateway/internal/store/dbgen"
)

// TokenRequest is a token request's form (RFC 6749 §4.1.3 and §6, with
// PKCE and a resource indicator).
type TokenRequest struct {
	GrantType    string
	ClientID     string
	Code         string
	RedirectURI  string
	CodeVerifier string
	RefreshToken string
	Resource     string
	RequestID    string
	ClientIP     string
}

// Tokens is a token response (RFC 6749 §5.1).
type Tokens struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
}

// Token answers the token endpoint.
func (s *Service) Token(ctx context.Context, r TokenRequest) (Tokens, error) {
	if r.ClientID == "" {
		return Tokens{}, &Error{Code: "invalid_client", Description: "Send client_id (public clients authenticate with it alone).", Status: http.StatusUnauthorized}
	}
	switch r.GrantType {
	case "authorization_code":
		return s.exchange(ctx, r)
	case "refresh_token":
		return s.refresh(ctx, r)
	}
	return Tokens{}, oauthError("unsupported_grant_type", "Only the authorization_code and refresh_token grant types are supported.")
}

// pkceVerifier is a code verifier's form (RFC 7636 §4.1).
var pkceVerifier = regexp.MustCompile(`^[A-Za-z0-9._~-]{43,128}$`)

// verifyPKCE checks a verifier against an S256 challenge, in constant time.
func verifyPKCE(verifier, challenge string) bool {
	if !pkceVerifier.MatchString(verifier) {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	return subtle.ConstantTimeCompare([]byte(base64.RawURLEncoding.EncodeToString(sum[:])), []byte(challenge)) == 1
}

// takeCode consumes a code: it is deleted before anything else is checked,
// so a code works once even when the exchange then fails.
func (s *Service) takeCode(ctx context.Context, code string) (dbgen.OauthCode, bool, error) {
	if !validTokenShape(code, codePrefix) {
		return dbgen.OauthCode{}, false, nil
	}
	row, err := s.q.TakeOAuthCode(ctx, s.digest(code))
	if errors.Is(store.NotFound(err), store.ErrNotFound) {
		return row, false, nil
	}
	return row, err == nil, err
}

// exchange redeems an authorization code (with its PKCE verifier) for tokens.
func (s *Service) exchange(ctx context.Context, r TokenRequest) (Tokens, error) {
	code, ok, err := s.takeCode(ctx, r.Code)
	if err != nil {
		return Tokens{}, err
	}
	switch {
	case !ok, !code.ExpiresAt.After(s.now()), code.ClientID != r.ClientID, code.RedirectUri != r.RedirectURI:
		return Tokens{}, errInvalidGrant()
	case r.Resource != "" && r.Resource != code.Resource:
		return Tokens{}, oauthError("invalid_target", "The resource must be the one the code was issued for: "+code.Resource)
	case !verifyPKCE(r.CodeVerifier, code.CodeChallenge):
		return Tokens{}, errInvalidGrant()
	}
	var out Tokens
	err = store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		g, err := s.usableGrant(ctx, q, code.GrantID)
		if err != nil {
			return err
		}
		if out, err = s.issue(ctx, q, g); err != nil {
			return err
		}
		return s.auditIssue(ctx, q, g, r, "authorization_code")
	})
	return out, err
}

// usableGrant locks a grant that is active and whose person is active.
// Access to the gateway is checked when a token is used.
func (s *Service) usableGrant(ctx context.Context, q *dbgen.Queries, id uuid.UUID) (dbgen.OauthGrant, error) {
	g, err := q.LockOAuthGrant(ctx, id)
	if errors.Is(store.NotFound(err), store.ErrNotFound) {
		return g, errInvalidGrant()
	} else if err != nil {
		return g, err
	}
	if g.RevokedAt != nil {
		return g, errInvalidGrant()
	}
	u, err := q.GetUser(ctx, g.UserID)
	if err != nil {
		return g, err
	}
	if u.Status != "active" {
		return g, errInvalidGrant()
	}
	return g, nil
}

// refresh rotates a refresh token. A token already rotated more than
// RefreshReuseGrace ago is reuse (a stolen copy, or a client that lost the
// new one): the whole grant is revoked, and the person connects the client
// again.
func (s *Service) refresh(ctx context.Context, r TokenRequest) (Tokens, error) {
	row, ok, err := s.lookup(ctx, r.RefreshToken, RefreshPrefix, kindRefresh)
	if err != nil {
		return Tokens{}, err
	}
	if !ok || row.OauthGrant.ClientID != r.ClientID {
		return Tokens{}, errInvalidGrant()
	}
	if r.Resource != "" && r.Resource != row.OauthToken.Resource {
		return Tokens{}, oauthError("invalid_target", "The resource must be the one the grant is for: "+row.OauthToken.Resource)
	}
	var (
		out    Tokens
		reused bool
	)
	err = store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		tok, err := q.LockOAuthToken(ctx, row.OauthToken.ID)
		if errors.Is(store.NotFound(err), store.ErrNotFound) {
			return errInvalidGrant()
		} else if err != nil {
			return err
		}
		if tok.RotatedAt != nil && s.now().Sub(*tok.RotatedAt) > RefreshReuseGrace {
			reused = true
			return s.revokeGrant(ctx, q, grantActor(row.OauthGrant, r.RequestID, r.ClientIP), row.OauthGrant.ID, ReasonReuse)
		}
		if !tok.ExpiresAt.After(s.now()) {
			return errInvalidGrant()
		}
		g, err := s.usableGrant(ctx, q, tok.GrantID)
		if err != nil {
			return err
		}
		// Within RefreshReuseGrace of its rotation the token was already
		// rotated: issue a new pair without rotating again.
		if tok.RotatedAt == nil {
			if n, err := q.RotateOAuthToken(ctx, tok.ID); err != nil || n == 0 {
				return errors.Join(err, errInvalidGrant())
			}
		}
		if out, err = s.issue(ctx, q, g); err != nil {
			return err
		}
		return s.auditIssue(ctx, q, g, r, "refresh_token")
	})
	if err == nil && reused {
		return Tokens{}, errInvalidGrant()
	}
	return out, err
}

// issue stores a new access and refresh token for a grant, bound to its
// resource.
func (s *Service) issue(ctx context.Context, q *dbgen.Queries, g dbgen.OauthGrant) (Tokens, error) {
	if err := q.DeleteExpiredOAuthTokens(ctx); err != nil {
		return Tokens{}, err
	}
	if g.ClientKind == KindRegistered {
		if err := q.TouchOAuthClient(ctx, g.ClientID); err != nil {
			return Tokens{}, err
		}
	}
	out := Tokens{AccessToken: newToken(AccessPrefix), RefreshToken: newToken(RefreshPrefix), TokenType: "Bearer",
		ExpiresIn: int(AccessTokenTTL.Seconds())}
	now := s.now()
	for _, t := range []struct {
		kind, token string
		ttl         time.Duration
	}{{kindAccess, out.AccessToken, AccessTokenTTL}, {kindRefresh, out.RefreshToken, RefreshTokenTTL}} {
		if err := q.InsertOAuthToken(ctx, dbgen.InsertOAuthTokenParams{
			GrantID: g.ID, Kind: t.kind, TokenHash: s.digest(t.token), Resource: g.Resource, ExpiresAt: now.Add(t.ttl),
		}); err != nil {
			return Tokens{}, err
		}
	}
	return out, nil
}

// grantActor is a grant's person acting through its client (audit).
func grantActor(g dbgen.OauthGrant, requestID, clientIP string) authz.Actor {
	return authz.Actor{UserID: g.UserID, PlatformRole: authz.PlatformNone, RequestID: requestID, ClientIP: clientIP}
}

// grantAudit is an audit entry by a grant's person through its client.
func grantAudit(a authz.Actor, g dbgen.OauthGrant, action string) audit.Entry {
	e := a.Audit(action, "oauth_grant", g.ID.String())
	e.Metadata = map[string]any{"via": "oauth", "client": g.ClientName, "clientId": g.ClientID, "gatewayId": g.GatewayID.String()}
	return e
}

// auditIssue records oauth.token_issue: the grant and the grant type,
// never a token.
func (s *Service) auditIssue(ctx context.Context, q *dbgen.Queries, g dbgen.OauthGrant, r TokenRequest, grantType string) error {
	e := grantAudit(grantActor(g, r.RequestID, r.ClientIP), g, "oauth.token_issue")
	e.Metadata["grantType"] = grantType
	return audit.Record(ctx, q, e)
}

// lookup finds a token of a kind by its digest, with its grant, person and
// gateway. ok is false for anything else.
func (s *Service) lookup(ctx context.Context, token, prefix, kind string) (dbgen.GetOAuthTokenRow, bool, error) {
	if !validTokenShape(token, prefix) {
		return dbgen.GetOAuthTokenRow{}, false, nil
	}
	row, err := s.q.GetOAuthToken(ctx, dbgen.GetOAuthTokenParams{TokenHash: s.digest(token), Kind: kind})
	if errors.Is(store.NotFound(err), store.ErrNotFound) {
		return row, false, nil
	}
	return row, err == nil, err
}

// ErrInvalidToken: the access token is unknown, expired or revoked, its
// grant or person is not active, or it is for another gateway.
var ErrInvalidToken = errors.New("oauth: invalid access token")

// Principal is who an access token acts as: a person through a client on
// one gateway.
type Principal struct {
	Grant dbgen.OauthGrant
	User  dbgen.User
	// GatewaySlug is the gateway the token is for.
	GatewaySlug string
}

// Authenticate checks an access token presented to /mcp/{slug}: its digest
// is looked up, then its expiry, its grant, its person (suspended:
// refused) and its audience, which must be that gateway (RFC 8707). Access
// to the gateway is checked by the caller, on every request.
func (s *Service) Authenticate(ctx context.Context, token, slug string) (Principal, error) {
	row, ok, err := s.lookup(ctx, token, AccessPrefix, kindAccess)
	if err != nil {
		return Principal{}, err
	}
	g := row.OauthGrant
	resource := s.Resource(slug)
	if !ok || !row.OauthToken.ExpiresAt.After(s.now()) || g.RevokedAt != nil || row.User.Status != "active" ||
		row.GatewaySlug != slug || row.OauthToken.Resource != resource || g.Resource != resource {
		return Principal{}, ErrInvalidToken
	}
	return Principal{Grant: g, User: row.User, GatewaySlug: row.GatewaySlug}, nil
}

// Touch records that a grant was used (at most once a minute).
func (s *Service) Touch(ctx context.Context, grantID uuid.UUID) error {
	return s.q.TouchOAuthGrant(ctx, grantID)
}

// Revoke is the revocation endpoint (RFC 7009): a refresh token revokes
// its whole grant; an access token only itself. A token of another client,
// or no token of ours, changes nothing (the answer is the same).
func (s *Service) Revoke(ctx context.Context, token, clientID, requestID, clientIP string) error {
	for _, k := range []struct{ prefix, kind string }{{RefreshPrefix, kindRefresh}, {AccessPrefix, kindAccess}} {
		row, ok, err := s.lookup(ctx, token, k.prefix, k.kind)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if row.OauthGrant.ClientID != clientID {
			return nil
		}
		a := grantActor(row.OauthGrant, requestID, clientIP)
		return store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
			if k.kind == kindRefresh {
				return s.revokeGrant(ctx, q, a, row.OauthGrant.ID, ReasonClient)
			}
			if err := q.DeleteOAuthToken(ctx, row.OauthToken.ID); err != nil {
				return err
			}
			e := grantAudit(a, row.OauthGrant, "oauth.revoke")
			e.Metadata["token"], e.Metadata["reason"] = kindAccess, ReasonClient
			return audit.Record(ctx, q, e)
		})
	}
	return nil
}
