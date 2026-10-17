package providers

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ncecere/open-mcp-gateway/internal/audit"
	"github.com/ncecere/open-mcp-gateway/internal/registry"
	"github.com/ncecere/open-mcp-gateway/internal/store"
	"github.com/ncecere/open-mcp-gateway/internal/store/dbgen"
)

// The runtime (decisions 27-29): a call of a capability with an effective
// requirement gets the person's access token for that provider, refreshed
// when it expires within a minute, or a consent link when there is no
// usable connection.

// RefreshAhead: a token expiring sooner than this is refreshed first.
const RefreshAhead = time.Minute

// Why a call needs consent (the connect request's reason, and the call
// record's error class).
const (
	ReasonNotConnected   = "not_connected"
	ReasonMissingScopes  = "missing_scopes"
	ReasonNeedsReconsent = "needs_reconsent"
)

// CallContext is the call that needs a credential.
type CallContext struct {
	User         dbgen.User
	Gateway      dbgen.Gateway
	CapabilityID uuid.UUID
	// Kind, ExposedName and Title describe the capability to the person.
	Kind, ExposedName, Title string
}

// Credential is what a call carries: the provider and the bare access
// token. The token goes only to the server whose capability requires it.
type Credential struct {
	Provider    registry.Provider
	AccessToken string
}

// ConsentNeeded: the person must connect (or reconnect) an account; URL is
// the consent link (the web app's /connect/{provider}?request=ID).
type ConsentNeeded struct {
	Reason, RequestID, URL string
	Provider               registry.Provider
	Scopes                 []string
}

func (e *ConsentNeeded) Error() string { return "providers: consent needed (" + e.Reason + ")" }

// ErrProviderDisabled: a platform admin turned the provider off (or it is
// gone): calls that need it are refused.
var ErrProviderDisabled = errors.New("providers: the auth provider is turned off")

// UnavailableError: the provider's token endpoint failed (network, 5xx,
// timeout); the connection is kept and the call isn't forwarded.
type UnavailableError struct {
	Provider registry.Provider
	Code     string
}

func (e *UnavailableError) Error() string { return "providers: provider unavailable (" + e.Code + ")" }

// errReconsent is a refresh that ended the connection's tokens.
var errReconsent = errors.New("providers: reconsent")

// Credential returns the person's token for a call that requires req, or
// a *ConsentNeeded (a consent link was made or reused), ErrProviderDisabled
// or an *UnavailableError. Nothing is sent anywhere but the provider's own
// token endpoint.
func (s *Service) Credential(ctx context.Context, req *registry.Requirement, cc CallContext) (Credential, error) {
	p, err := s.q.GetAuthProvider(ctx, req.Provider.ID)
	if errors.Is(store.NotFound(err), store.ErrNotFound) {
		return Credential{}, ErrProviderDisabled
	} else if err != nil {
		return Credential{}, err
	}
	if !p.Enabled {
		return Credential{}, ErrProviderDisabled
	}
	prov := registry.ProviderOf(p)
	c, err := s.q.GetProviderConnection(ctx, dbgen.GetProviderConnectionParams{UserID: cc.User.ID, ProviderID: p.ID})
	switch {
	case errors.Is(store.NotFound(err), store.ErrNotFound):
		return Credential{}, s.needConsent(ctx, prov, req.Scopes, cc, ReasonNotConnected)
	case err != nil:
		return Credential{}, err
	case c.Status != StatusActive:
		return Credential{}, s.needConsent(ctx, prov, req.Scopes, cc, ReasonNeedsReconsent)
	case !Covers(c.Scopes, req.Scopes):
		return Credential{}, s.needConsent(ctx, prov, req.Scopes, cc, ReasonMissingScopes)
	}
	if c.ExpiresAt != nil && c.ExpiresAt.Before(s.now().Add(RefreshAhead)) {
		c, err = s.refreshConnection(ctx, p, c)
		if errors.Is(err, errReconsent) {
			return Credential{}, s.needConsent(ctx, prov, req.Scopes, cc, ReasonNeedsReconsent)
		} else if err != nil {
			return Credential{}, err
		}
	}
	token, err := s.open(c.ID, "access", c.AccessTokenCipher)
	if err != nil || token == "" {
		// Sealed with a key this installation no longer has.
		s.Log.WarnContext(ctx, "provider token unreadable; asking to reconnect", "provider", p.Slug)
		if err := s.markReconsent(ctx, s.q, p, c, "config"); err != nil {
			return Credential{}, err
		}
		return Credential{}, s.needConsent(ctx, prov, req.Scopes, cc, ReasonNeedsReconsent)
	}
	if err := s.q.TouchProviderConnection(ctx, c.ID); err != nil {
		s.Log.WarnContext(ctx, "could not record a connection's use", "err", err)
	}
	return Credential{Provider: prov, AccessToken: token}, nil
}

// refreshConnection refreshes a connection's access token while holding its
// row lock, so replicas never use a (rotating) refresh token twice: a
// replica that waited finds the token already refreshed. A refused refresh
// (invalid_grant, 400, 401) or an expired token without a refresh token
// erases the tokens (needs_reconsent; errReconsent); a provider that can't
// be reached keeps the connection (*UnavailableError).
func (s *Service) refreshConnection(ctx context.Context, p dbgen.AuthProvider, conn dbgen.ProviderConnection) (dbgen.ProviderConnection, error) {
	var out dbgen.ProviderConnection
	var outcome error
	err := store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		c, err := q.LockProviderConnectionByID(ctx, conn.ID)
		if errors.Is(store.NotFound(err), store.ErrNotFound) {
			outcome = errReconsent
			return nil
		} else if err != nil {
			return err
		}
		switch {
		case c.Status != StatusActive:
			outcome = errReconsent
			return nil
		case c.ExpiresAt == nil || !c.ExpiresAt.Before(s.now().Add(RefreshAhead)):
			out = c // another request refreshed it meanwhile
			return nil
		}
		rt, err := s.open(c.ID, "refresh", c.RefreshTokenCipher)
		if err != nil || rt == "" {
			outcome = errReconsent
			return s.markReconsent(ctx, q, p, c, "expired")
		}
		tok, ee := s.refresh(ctx, p, rt)
		if ee != nil {
			if ee.Unavailable {
				s.Log.InfoContext(ctx, "auth provider refresh failed", "provider", p.Slug, "error", ee.Code, "status", ee.Status)
				outcome = &UnavailableError{Provider: registry.ProviderOf(p), Code: ee.Code}
				return nil
			}
			outcome = errReconsent
			return s.markReconsent(ctx, q, p, c, ee.Code)
		}
		access, err := s.seal(c.ID, "access", tok.Access)
		if err != nil {
			return err
		}
		refresh := c.RefreshTokenCipher
		if tok.Refresh != "" {
			// Rotated: the new refresh token replaces the old one.
			if refresh, err = s.seal(c.ID, "refresh", tok.Refresh); err != nil {
				return err
			}
		}
		scopes := nonNil(c.Scopes)
		if tok.HasScope && len(tok.Scopes) > 0 {
			scopes = tok.Scopes
		}
		var expires *time.Time
		if tok.ExpiresIn > 0 {
			t := s.now().Add(time.Duration(tok.ExpiresIn) * time.Second)
			expires = &t
		}
		out, err = q.SetProviderConnectionRefreshed(ctx, dbgen.SetProviderConnectionRefreshedParams{
			ID: c.ID, AccessTokenCipher: access, RefreshTokenCipher: refresh, ExpiresAt: expires, Scopes: scopes,
		})
		return err
	})
	if err != nil {
		return out, err
	}
	return out, outcome
}

// markReconsent erases a connection's tokens. Audited as
// provider_connection.refresh_failed by the system, with the reason.
func (s *Service) markReconsent(ctx context.Context, q *dbgen.Queries, p dbgen.AuthProvider, c dbgen.ProviderConnection, reason string) error {
	if err := q.MarkProviderConnectionReconsent(ctx, dbgen.MarkProviderConnectionReconsentParams{ID: c.ID, ErrorClass: &reason}); err != nil {
		return err
	}
	name := p.Name
	if u, err := q.GetUser(ctx, c.UserID); err == nil {
		name = displayName(u) + " · " + p.Name
	}
	return audit.Record(ctx, q, audit.Entry{
		ActorKind: audit.ActorSystem, Action: "provider_connection.refresh_failed", TargetType: "provider_connection",
		TargetID: c.ID.String(), Before: connectionSnapshot(c),
		Metadata: map[string]any{"name": name, "reason": reason, "provider": p.Slug, "userId": c.UserID.String()},
	})
}

// needConsent makes (or reuses, for the same person, capability and
// scopes) a consent link and returns it as a *ConsentNeeded.
func (s *Service) needConsent(ctx context.Context, p registry.Provider, scopes []string, cc CallContext, reason string) error {
	id, err := s.request(ctx, p, scopes, cc, reason)
	if err != nil {
		return err
	}
	return &ConsentNeeded{Reason: reason, RequestID: id, URL: s.ConnectURL(p.Slug, id), Provider: p, Scopes: slices.Clone(scopes)}
}

// request returns the ID of an open consent link for the call, making one
// when none stays valid for at least two more minutes. A person has at most
// MaxOpenRequests open; the oldest is dropped.
func (s *Service) request(ctx context.Context, p registry.Provider, scopes []string, cc CallContext, reason string) (string, error) {
	scopes = nonNil(scopes)
	var id string
	err := store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		open, err := q.FindOpenConnectRequest(ctx, dbgen.FindOpenConnectRequestParams{UserID: cc.User.ID, ProviderID: p.ID,
			GatewayID: cc.Gateway.ID, CapabilityID: cc.CapabilityID, RequiredScopes: scopes, ValidUntil: s.now().Add(2 * time.Minute)})
		if err == nil {
			if b, err := s.box.Open(open.IDCipher, requestAAD(open.IDDigest)); err == nil {
				id = string(b)
				return nil
			}
		} else if !errors.Is(store.NotFound(err), store.ErrNotFound) {
			return err
		}
		id = randomID()
		digest := requestDigest(id)
		ct, err := s.box.Seal([]byte(id), requestAAD(digest))
		if err != nil {
			return err
		}
		title := []rune(cc.Title)
		if len(title) > 200 {
			title = title[:200]
		}
		if _, err := q.InsertConnectRequest(ctx, dbgen.InsertConnectRequestParams{
			IDDigest: digest, IDCipher: ct, UserID: cc.User.ID, ProviderID: p.ID, RequiredScopes: scopes, Reason: reason,
			GatewayID: cc.Gateway.ID, GatewaySlug: cc.Gateway.Slug, GatewayName: cc.Gateway.Name, CapabilityID: cc.CapabilityID,
			CapabilityKind: cc.Kind, ExposedName: cc.ExposedName, CapabilityTitle: string(title), ExpiresAt: s.now().Add(RequestTTL),
		}); err != nil {
			return err
		}
		if _, err := q.TrimOpenConnectRequests(ctx, dbgen.TrimOpenConnectRequestsParams{UserID: cc.User.ID, Keep: MaxOpenRequests}); err != nil {
			return err
		}
		_, err = q.DeleteStaleConnectRequests(ctx, s.now().Add(-requestKeep))
		return err
	})
	return id, err
}
