package oauth

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ncecere/open-mcp-portal/internal/apperr"
	"github.com/ncecere/open-mcp-portal/internal/audit"
	"github.com/ncecere/open-mcp-portal/internal/authz"
	"github.com/ncecere/open-mcp-portal/internal/store"
	"github.com/ncecere/open-mcp-portal/internal/store/dbgen"
)

// Why a grant was revoked (oauth_grants.revoked_reason, audit metadata).
const (
	ReasonUser   = "user"
	ReasonAdmin  = "admin"
	ReasonClient = "client"
	ReasonReuse  = "refresh_reuse"
)

// Grant is a connected app: a person's consent for a client on a gateway,
// with the gateway's slug and name.
type Grant = dbgen.ListUserOAuthGrantsRow

var (
	errNoGrant  = apperr.NotFound("grant_not_found", "Connected app not found")
	errNoPerson = apperr.NotFound("user_not_found", "User not found")
)

// ListGrants lists a person's connected apps, newest first: their own, or
// anyone's for platform admins and auditors.
func (s *Service) ListGrants(ctx context.Context, a authz.Actor, userID uuid.UUID) ([]Grant, error) {
	if a.UserID != userID {
		if !a.CanReadPlatform() {
			return nil, errNoPerson
		}
		if _, err := s.q.GetUser(ctx, userID); errors.Is(store.NotFound(err), store.ErrNotFound) {
			return nil, errNoPerson
		} else if err != nil {
			return nil, err
		}
	}
	return s.q.ListUserOAuthGrants(ctx, userID)
}

// RevokeGrant disconnects an app: the person themselves, or a platform
// admin (an auditor may only look). Its tokens stop working at once and its
// consent is forgotten. Audited as oauth.revoke (reason user or admin).
func (s *Service) RevokeGrant(ctx context.Context, a authz.Actor, userID, grantID uuid.UUID) error {
	reason := ReasonUser
	if a.UserID != userID {
		if !a.IsPlatformAdmin() {
			if a.CanReadPlatform() {
				return apperr.Forbidden("Only platform admins can disconnect another person's apps")
			}
			return errNoGrant
		}
		reason = ReasonAdmin
	}
	return store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		g, err := q.LockOAuthGrant(ctx, grantID)
		if errors.Is(store.NotFound(err), store.ErrNotFound) || (err == nil && (g.UserID != userID || g.RevokedAt != nil)) {
			return errNoGrant
		} else if err != nil {
			return err
		}
		return s.revokeGrant(ctx, q, a, g.ID, reason)
	})
}

// revokeGrant revokes a grant and deletes its codes and tokens, auditing
// oauth.revoke with the reason. A grant already revoked is left alone.
func (s *Service) revokeGrant(ctx context.Context, q *dbgen.Queries, a authz.Actor, id uuid.UUID, reason string) error {
	g, err := q.LockOAuthGrant(ctx, id)
	if err != nil {
		return err
	}
	n, err := q.RevokeOAuthGrant(ctx, dbgen.RevokeOAuthGrantParams{ID: id, Reason: &reason})
	if err != nil || n == 0 {
		return err
	}
	if err := q.DeleteOAuthGrantCodes(ctx, id); err != nil {
		return err
	}
	if err := q.DeleteOAuthGrantTokens(ctx, id); err != nil {
		return err
	}
	gw, err := q.GetGateway(ctx, g.GatewayID)
	if err != nil {
		return err
	}
	e := grantAudit(a, g, "oauth.revoke")
	if gw.TeamID.Valid {
		e.TeamID = gw.TeamID.UUID
	}
	e.Metadata["reason"] = reason
	e.Metadata["name"] = g.ClientName + " · " + gw.Name
	if reason == ReasonUser || reason == ReasonAdmin {
		delete(e.Metadata, "via") // done in the app, not by the client
	}
	e.Before = grantSnapshot(g, gw)
	return audit.Record(ctx, q, e)
}

// GrantClient is the client of a grant as it was when consent was given.
func GrantClient(g dbgen.OauthGrant) Client {
	return Client{ID: g.ClientID, Kind: g.ClientKind, Name: g.ClientName, URI: deref(g.ClientUri), LogoURI: deref(g.LogoUri)}
}

// TrustedIDs is the trusted-client list now, by client ID.
func (s *Service) TrustedIDs(ctx context.Context) (map[string]bool, error) { return s.trustedIDs(ctx) }
