package automation

import (
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ncecere/open-mcp-portal/internal/apperr"
	"github.com/ncecere/open-mcp-portal/internal/audit"
	"github.com/ncecere/open-mcp-portal/internal/authz"
	"github.com/ncecere/open-mcp-portal/internal/store/dbgen"
)

func TestNormalizeScopes(t *testing.T) {
	ok := []struct {
		kind string
		in   []string
		want []string
	}{
		{authz.TokenPlatformAdmin, []string{"platform:read"}, []string{"platform:read"}},
		{authz.TokenPlatformAdmin, []string{"platform:write"}, []string{"platform:read", "platform:write"}},
		{authz.TokenPlatformAdmin, []string{"platform:write", "platform:read", "platform:write"}, []string{"platform:read", "platform:write"}},
		{authz.TokenTeamPublisher, []string{"servers:write"}, []string{"servers:read", "servers:write"}},
		{authz.TokenTeamPublisher, []string{"servers:read"}, []string{"servers:read"}},
	}
	for _, c := range ok {
		got, err := NormalizeScopes(c.kind, c.in)
		if err != nil || !slices.Equal(got, c.want) {
			t.Errorf("%s %v = %v, %v; want %v", c.kind, c.in, got, err, c.want)
		}
	}
	bad := []struct {
		kind string
		in   []string
	}{
		{authz.TokenPlatformAdmin, nil},
		{authz.TokenPlatformAdmin, []string{"servers:read"}},
		{authz.TokenTeamPublisher, []string{"platform:read"}},
		{authz.TokenTeamPublisher, []string{"servers:read", "platform:write"}},
		{authz.TokenTeamPublisher, []string{"servers:delete"}},
		{authz.TokenPlatformAdmin, []string{""}},
	}
	for _, c := range bad {
		_, err := NormalizeScopes(c.kind, c.in)
		if e, _ := apperr.As(err); e == nil || e.Code != "invalid_scopes" || e.Status != 400 {
			t.Errorf("%s %v: %v, want 400 invalid_scopes", c.kind, c.in, err)
		}
	}
}

func TestStatusAndBlocked(t *testing.T) {
	now := time.Now()
	revoked := now.Add(-time.Hour)
	mk := func(kind string, exp time.Time, rev *time.Time, admin bool) Token {
		return Token{Row: dbgen.AutomationToken{Kind: kind, ExpiresAt: exp, RevokedAt: rev}, CreatorIsAdmin: admin}
	}
	cases := []struct {
		t       Token
		status  string
		blocked string
	}{
		{mk(authz.TokenPlatformAdmin, now.Add(time.Hour), nil, true), StatusActive, ""},
		{mk(authz.TokenPlatformAdmin, now.Add(time.Hour), nil, false), StatusActive, BlockedCreatorNotAdmin},
		{mk(authz.TokenTeamPublisher, now.Add(time.Hour), nil, false), StatusActive, ""}, // team tokens outlive their creator
		{mk(authz.TokenPlatformAdmin, now.Add(-time.Second), nil, false), StatusExpired, ""},
		{mk(authz.TokenPlatformAdmin, now.Add(time.Hour), &revoked, false), StatusRevoked, ""},
		{mk(authz.TokenPlatformAdmin, now, nil, true), StatusExpired, ""}, // the boundary: expires_at is exclusive
	}
	for i, c := range cases {
		if got := c.t.Status(now); got != c.status {
			t.Errorf("case %d status = %s, want %s", i, got, c.status)
		}
		if got := c.t.BlockedReason(now); got != c.blocked {
			t.Errorf("case %d blocked = %q, want %q", i, got, c.blocked)
		}
	}
}

func TestPrincipalActor(t *testing.T) {
	team := uuid.New()
	p := func(kind string, scopes ...string) Principal {
		row := dbgen.AutomationToken{ID: uuid.New(), Kind: kind, Name: "ci", Scopes: scopes}
		if kind == authz.TokenTeamPublisher {
			row.TeamID = uuid.NullUUID{UUID: team, Valid: true}
		}
		return Principal{Token: Token{Row: row}}
	}
	read := p(authz.TokenPlatformAdmin, "platform:read").Actor("req", "203.0.113.1")
	write := p(authz.TokenPlatformAdmin, "platform:read", "platform:write").Actor("req", "203.0.113.1")
	if !read.IsToken() || read.UserID != uuid.Nil || read.PlatformRole != authz.PlatformAuditor || read.IsPlatformAdmin() || !read.CanReadPlatform() {
		t.Errorf("platform:read acts as an auditor: %+v", read)
	}
	if write.PlatformRole != authz.PlatformAdmin || !write.IsPlatformAdmin() {
		t.Errorf("platform:write acts as a platform admin: %+v", write)
	}
	pr := p(authz.TokenTeamPublisher, "servers:read").Actor("r", "")
	pw := p(authz.TokenTeamPublisher, "servers:read", "servers:write").Actor("r", "")
	other := uuid.New()
	for _, c := range []struct {
		a    authz.Actor
		team uuid.UUID
		want string
	}{{pr, team, authz.RoleMember}, {pw, team, authz.RoleAdmin}, {pr, other, ""}, {pw, other, ""}, {read, team, ""}, {write, team, ""}} {
		// A token's role never depends on a membership row.
		if got := c.a.TeamRole(c.team, authz.RoleOwner); got != c.want {
			t.Errorf("%s %v TeamRole = %q, want %q", c.a.TokenKind, c.a.TokenScopes, got, c.want)
		}
	}
	if pw.PlatformRole != authz.PlatformNone || pw.CanReadPlatform() {
		t.Error("a publisher token has no platform standing")
	}
	// A person's role is the membership role.
	if (authz.Actor{}).TeamRole(team, authz.RoleEditor) != authz.RoleEditor {
		t.Error("a person's role is their membership")
	}
}

func TestActorAuditNamesTheToken(t *testing.T) {
	id := uuid.New()
	a := authz.Actor{Kind: authz.KindAutomationToken, TokenID: id, TokenName: "ci", TokenKind: authz.TokenTeamPublisher, RequestID: "r"}
	e := a.Audit("mcp_server.create", "mcp_server", "x")
	if e.ActorKind != audit.ActorAutomationToken || e.ActorUserID != uuid.Nil || e.ActorTokenID != id || e.ActorTokenName != "ci" || e.ActorTokenKind != authz.TokenTeamPublisher {
		t.Errorf("entry = %+v", e)
	}
}

func TestDigestIsPepperKeyed(t *testing.T) {
	a, b := Digest([]byte("pepper-one"), "ompadm_x"), Digest([]byte("pepper-two"), "ompadm_x")
	if a == b || len(a) != 64 || Digest([]byte("pepper-one"), "ompadm_x") != a {
		t.Error("digest must be a stable 64-hex HMAC under the pepper")
	}
}
