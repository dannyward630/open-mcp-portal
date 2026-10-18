package registry

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"github.com/ncecere/open-mcp-portal/internal/authz"
	"github.com/ncecere/open-mcp-portal/internal/identity"
	"github.com/ncecere/open-mcp-portal/internal/store/dbgen"
	"github.com/ncecere/open-mcp-portal/internal/upstream"
)

// IdentitySigner signs identity tokens (*identity.Service).
type IdentitySigner interface {
	Sign(ctx context.Context, c identity.Claims) (string, error)
}

// SetIdentity sets the signer a manual Test uses. Without one, Test sends no
// identity token (as before v0.2.1).
func (s *Service) SetIdentity(ids IdentitySigner) { s.ids = ids }

// What the identity token of a Test says where a call would name a gateway
// and a tool (decision 61). A Test goes through no gateway and calls no tool,
// so exactly one of tool, prompt and resource is NOT set (the claims doc says
// "exactly one" about calls); gateway is a value no gateway slug can have (a
// slug is lowercase letters, digits and hyphens), so a server that decides
// by gateway can't mistake a test for traffic from one; client says what it is.
const (
	TestGateway = "omp:test"
	TestClient  = "Test connection"
)

// identityRefused is the message of a Test that failed with the identity
// token and passed without it. The HTTP status is in the result's own field.
const identityRefused = "The server accepts the portal's requests without its identity token but refused them with it. " +
	"Turn off Send identity token for this server, or have the server accept the token."

// testIdentity signs the token a manual Test sends: the identity of the
// person who pressed the button, for the server's URL, the way a call from
// them would be signed (principal_type user, their ID, email, name and team
// slugs; no gateway and no tool, see TestGateway). It returns "" when none
// is sent: Send identity is off, no signer is set, or the actor is an
// automation token (no person; the Test then behaves as it did before).
//
// Only the manual Test sends it, because only it answers "would a real call
// work?". Scheduled health checks must keep working without any caller (and
// would put a token naming nobody, every few minutes, in front of a
// third party), and Refresh stores what the server lists, which must not
// depend on who asked: some servers list differently for an authenticated
// caller, and the portal's approvals are of what the server says to anyone.
func (s *Service) testIdentity(ctx context.Context, a authz.Actor, sv dbgen.McpServer) (string, *upstream.Error) {
	if !sv.SendIdentity || s.ids == nil || a.IsToken() || a.UserID == uuid.Nil {
		return "", nil
	}
	fail := func(err error) (string, *upstream.Error) {
		s.log.WarnContext(ctx, "could not sign the identity token of an MCP server test", "server", sv.ID, "err", err)
		return "", &upstream.Error{Class: upstream.ClassConfig, Message: "The portal couldn't sign an identity token for this test."}
	}
	u, err := s.q.GetUser(ctx, a.UserID)
	if err != nil {
		return fail(err)
	}
	rows, err := s.q.ListTeamsForUser(ctx, u.ID)
	if err != nil {
		return fail(err)
	}
	slugs := make([]string, len(rows))
	for i, t := range rows {
		slugs[i] = t.Slug
	}
	token, err := s.ids.Sign(ctx, identity.Claims{Audience: sv.URL, PrincipalType: identity.PrincipalUser, Subject: u.ID.String(),
		Email: u.Email, Name: u.DisplayName, Teams: slugs, Gateway: TestGateway, Client: TestClient})
	if err != nil {
		return fail(err)
	}
	return token, nil
}

// testAs is a manual Test: a read with the identity token, and when that is
// refused with 401 or 403, the same read without it. If only the second
// passes, the identity token is what the server refuses, and the result says
// so (class auth, the server's HTTP status): turn Send identity token off or
// have the server accept it. Both failing, or the first passing, is the
// ordinary result.
//
// This catches a server that refuses the request itself. It does not catch
// one that accepts it and rejects the token only when a tool runs (HTTP 200
// with isError): that passes, like any server whose tools fail for their
// own reasons. Nothing here reads or rewrites tool results.
func (s *Service) testAs(ctx context.Context, a authz.Actor, sv dbgen.McpServer) TestResult {
	token, fail := s.testIdentity(ctx, a, sv)
	if fail != nil {
		return TestResult{Err: fail}
	}
	res, _, _ := s.probeAs(ctx, sv, TriggerManual, token)
	if token == "" || res.OK || res.Err == nil || res.Err.Class != upstream.ClassAuth ||
		(res.Err.HTTPStatus != http.StatusUnauthorized && res.Err.HTTPStatus != http.StatusForbidden) {
		return res
	}
	if plain, _, _ := s.probeAs(ctx, sv, TriggerManual, ""); !plain.OK {
		return res
	}
	res.Err = &upstream.Error{Class: upstream.ClassAuth, HTTPStatus: res.Err.HTTPStatus, Message: identityRefused}
	return res
}
