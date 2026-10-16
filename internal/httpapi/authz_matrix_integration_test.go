package httpapi_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"sort"
	"strings"
	"testing"

	"github.com/ncecere/open-mcp-gateway/internal/auth"
	"github.com/ncecere/open-mcp-gateway/internal/httpx"
	"github.com/ncecere/open-mcp-gateway/internal/testutil"
)

// The authorization matrix. Every operation in api/openapi.yaml is
// classified in matrixPolicies (authz_matrix_policy_test.go); this test calls
// every operation as every kind of caller:
//
//   - on the caller's own team (B), or globally: success exactly for the
//     classified callers, and 401, 403 or 404 for everyone else;
//   - on team A, of which the team-role callers are not members: denied for
//     everyone but the classified platform roles, and never with team A's
//     name or ID in a denied response.

// teamFix is one team's fixture.
type teamFix struct {
	slug, id, name string
	member         string // a member (role member) the callers are not, for update/remove
	invite         string // an open invite
	auditID        string // an entry of the team's audit log
	// server is a published, enabled MCP server with a changed capability
	// (capability), for reads and refused writes.
	server, capability string
	// gateway is a team gateway.
	gateway string
}

type matrixEnv struct {
	t       *testing.T
	app     *testApp
	callers [numCallers]*session
	userIDs [numCallers]string
	a, b    teamFix
	// spareUser and spareTeam are targets of platform writes that are
	// refused; allowed writes get fresh ones.
	spareUser, spareTeam string
	// fakeURL is a fake MCP server every fixture server points at.
	fakeURL string
	// platformGateway is granted to team B and to every platform caller.
	platformGateway string
	n               int
}

func (e *matrixEnv) next() int { e.n++; return e.n }

func (e *matrixEnv) exec(sql string, args ...any) string {
	e.t.Helper()
	var out string
	if err := e.app.Pool.QueryRow(context.Background(), sql, args...).Scan(&out); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	return out
}

// newUser inserts a user (as a sign-in would) and returns its ID.
func (e *matrixEnv) newUser(email, role string) string {
	return e.exec(`INSERT INTO users (oidc_issuer, oidc_subject, email, display_name, platform_role)
		VALUES ('matrix', $1, $2, $3, $4) RETURNING id::text`, email, email, email, role)
}

// sessionFor creates a browser session for a user directly in the database
// (the matrix needs more people than the development personas).
func (e *matrixEnv) sessionFor(userID string) *session {
	e.t.Helper()
	secret, csrf := httpx.NewID(32), httpx.NewID(32)
	e.exec(`INSERT INTO sessions (user_id, token_digest, csrf_token, expires_at)
		VALUES ($1, $2, $3, now() + interval '1 hour') RETURNING id::text`, userID, auth.Digest(secret), csrf)
	jar, _ := cookiejar.New(nil)
	jar.SetCookies(mustParse(e.t, e.app.URL), []*http.Cookie{{Name: auth.SessionCookieName(false), Value: secret, Path: "/"}})
	return &session{t: e.t, app: e.app, client: &http.Client{Jar: jar}, csrf: csrf}
}

func anonSession(t *testing.T, app *testApp) *session {
	jar, _ := cookiejar.New(nil)
	return &session{t: t, app: app, client: &http.Client{Jar: jar}}
}

func (e *matrixEnv) newTeam(slug, name string) teamFix {
	tf := teamFix{slug: slug, name: name}
	tf.id = e.exec(`INSERT INTO teams (slug, name) VALUES ($1, $2) RETURNING id::text`, slug, name)
	tf.member = e.newMember(tf, "member")
	tf.invite = e.newInvite(tf)
	tf.auditID = e.exec(`INSERT INTO audit_log (actor_kind, team_id, action, target_type, target_id)
		VALUES ('system', $1::uuid, 'team.create', 'team', $1::text) RETURNING id::text`, tf.id)
	tf.server = e.newServer(tf, "published")
	tf.capability = e.newChangedCapability(tf.server)
	tf.gateway = e.newGateway(tf.id)
	return tf
}

// newGateway inserts a gateway: a team gateway of teamID, or a platform
// gateway for "".
func (e *matrixEnv) newGateway(teamID string) string {
	n := e.next()
	kind := "team"
	if teamID == "" {
		kind = "platform"
	}
	return e.exec(`INSERT INTO gateways (slug, name, owner_kind, team_id) VALUES ($1, $2, $3, NULLIF($4, '')::uuid) RETURNING id::text`,
		fmt.Sprintf("gw-%d", n), fmt.Sprintf("Gateway %d", n), kind, teamID)
}

// newGrant inserts a group grant of a fresh platform gateway.
func (e *matrixEnv) newGrant() (gateway, grant string) {
	gateway = e.newGateway("")
	return gateway, e.exec(`INSERT INTO gateway_grants (gateway_id, subject_kind, group_name) VALUES ($1, 'group', 'g') RETURNING id::text`, gateway)
}

// newServer inserts a refreshed MCP server of the team in a publish state,
// pointing at the fake MCP server.
func (e *matrixEnv) newServer(tf teamFix, state string) string {
	n := e.next()
	return e.exec(`INSERT INTO mcp_servers (team_id, slug, name, url, publish_state, published_at, refreshed_at)
		VALUES ($1, $2, $3, $4, $5::text, CASE WHEN $5::text = 'published' THEN now() END, now()) RETURNING id::text`,
		tf.id, fmt.Sprintf("srv-%d", n), fmt.Sprintf("Server %d", n), e.fakeURL, state)
}

// newChangedCapability inserts a tool whose definition changed since it was
// approved.
func (e *matrixEnv) newChangedCapability(serverID string) string {
	return e.exec(`INSERT INTO mcp_capabilities (server_id, kind, name, definition, fingerprint, approved_fingerprint)
		VALUES ($1, 'tool', 'fixture_tool', '{"name":"fixture_tool"}', repeat('a', 64), repeat('b', 64)) RETURNING id::text`, serverID)
}

func (e *matrixEnv) newMember(tf teamFix, role string) string {
	id := e.newUser(fmt.Sprintf("m-%d@example.com", e.next()), "none")
	e.exec(`INSERT INTO team_members (team_id, user_id, role) VALUES ($1, $2, $3) RETURNING role`, tf.id, id, role)
	return id
}

func (e *matrixEnv) newInvite(tf teamFix) string {
	return e.exec(`INSERT INTO team_invites (team_id, email, role, expires_at)
		VALUES ($1, $2, 'member', now() + interval '1 day') RETURNING id::text`, tf.id, fmt.Sprintf("i-%d@example.com", e.next()))
}

func newMatrixEnv(t *testing.T) *matrixEnv {
	e := &matrixEnv{t: t, app: newTestApp(t, nil), fakeURL: testutil.NewFakeMCP(t).URL()}
	e.a = e.newTeam("alpha", "Alpha Confidential Team")
	e.b = e.newTeam("bravo", "Bravo Team")
	e.exec(`UPDATE teams SET publish_policy_override = 'self_publish' WHERE id = $1 RETURNING slug`, e.b.id)
	e.newMember(e.a, "owner")
	roles := map[caller]string{cMember: "member", cEditor: "editor", cTeamAdmin: "admin", cOwner: "owner"}
	platformRoles := map[caller]string{cPlatformAdmin: "platform_admin", cAuditor: "platform_auditor"}
	for c := cMember; c < numCallers; c++ {
		prole := platformRoles[c]
		if prole == "" {
			prole = "none"
		}
		id := e.newUser(fmt.Sprintf("%s@example.com", c), prole)
		if r, ok := roles[c]; ok {
			e.exec(`INSERT INTO team_members (team_id, user_id, role) VALUES ($1, $2, $3) RETURNING role`, e.b.id, id, r)
		}
		e.userIDs[c] = id
		e.callers[c] = e.sessionFor(id)
	}
	e.callers[cAnon] = anonSession(t, e.app)
	e.platformGateway = e.newGateway("")
	e.exec(`INSERT INTO gateway_grants (gateway_id, subject_kind, team_id) VALUES ($1, 'team', $2) RETURNING id::text`, e.platformGateway, e.b.id)
	for _, c := range []caller{cPlatformAdmin, cAuditor} {
		e.exec(`INSERT INTO gateway_grants (gateway_id, subject_kind, user_id) VALUES ($1, 'user', $2) RETURNING id::text`, e.platformGateway, e.userIDs[c])
	}
	e.spareUser = e.newUser("spare@example.com", "none")
	e.spareTeam = e.newTeam("spare", "Spare Team").slug
	return e
}

// mctx is what a request builder sees.
type mctx struct {
	e       *matrixEnv
	tf      *teamFix
	who     caller
	allowed bool // expected to succeed: destructive calls get fresh objects
}

func (c *mctx) team(suffix string) string { return "/v1/teams/" + c.tf.slug + suffix }

func (c *mctx) member() string {
	if c.allowed {
		return c.e.newMember(*c.tf, "member")
	}
	return c.tf.member
}

func (c *mctx) invite() string {
	if c.allowed {
		return c.e.newInvite(*c.tf)
	}
	return c.tf.invite
}

// server is a fresh server of the team in state when the call is expected
// to succeed, else the team's fixture server.
func (c *mctx) server(state string) string {
	if c.allowed {
		return c.e.newServer(*c.tf, state)
	}
	return c.tf.server
}

// changedCapability is a published server and its changed capability:
// fresh ones when the call is expected to succeed.
func (c *mctx) changedCapability() (server, capability string) {
	if c.allowed {
		srv := c.e.newServer(*c.tf, "published")
		return srv, c.e.newChangedCapability(srv)
	}
	return c.tf.server, c.tf.capability
}

// gateway is a fresh gateway of the team when the call is expected to
// succeed, else the team's fixture gateway.
func (c *mctx) gateway() string {
	if c.allowed {
		return c.e.newGateway(c.tf.id)
	}
	return c.tf.gateway
}

// platformGateway is a fresh platform gateway when the call is expected
// to succeed, else the fixture one.
func (c *mctx) platformGateway() string {
	if c.allowed {
		return c.e.newGateway("")
	}
	return c.e.platformGateway
}

// grant is a platform gateway's grant: a fresh one when the call is
// expected to succeed.
func (c *mctx) grant() (gateway, grant string) {
	if c.allowed {
		return c.e.newGrant()
	}
	return c.e.platformGateway, c.e.exec(`SELECT id::text FROM gateway_grants WHERE gateway_id = $1 LIMIT 1`, c.e.platformGateway)
}

// apiKey is a fresh key of the caller when the call is expected to
// succeed, else another person's key.
func (c *mctx) apiKey() string {
	owner := c.e.spareUser
	if c.allowed {
		owner = c.e.userIDs[c.who]
	}
	n := c.e.next()
	return c.e.exec(`INSERT INTO api_keys (user_id, name, prefix, digest, expires_at)
		VALUES ($1, 'k', 'omcp_matrix00', encode(sha256($2::bytea), 'hex'), now() + interval '1 day') RETURNING id::text`,
		owner, fmt.Sprintf("matrix-%d", n))
}

func (c *mctx) user() string {
	if c.allowed {
		return c.e.newUser(fmt.Sprintf("u-%d@example.com", c.e.next()), "none")
	}
	return c.e.spareUser
}

func (c *mctx) spareTeam() string {
	if c.allowed {
		return c.e.newTeam(fmt.Sprintf("fresh-%d", c.e.next()), "Fresh").slug
	}
	return c.e.spareTeam
}

// call runs one request as who and checks the outcome.
func (e *matrixEnv) call(t *testing.T, op string, p policy, tf *teamFix, who caller, allowed, foreign bool) {
	t.Helper()
	c := &mctx{e: e, tf: tf, who: who, allowed: allowed}
	req := p.build(c)
	s := e.callers[who]
	switch {
	case req.anon:
		s = anonSession(t, e.app)
	case req.ownSession && who != cAnon:
		s = e.sessionFor(e.userIDs[who])
	}
	code, body := s.raw(req.method, req.path, req.body, req.headers)
	where := "own"
	if foreign {
		where = "team A"
	}
	label := fmt.Sprintf("%s as %s on %s (%s %s)", op, who, where, req.method, req.path)
	switch {
	case code >= 500:
		t.Errorf("%s: %d %s", label, code, body)
	case p.anyStatus:
	case allowed && (code < 200 || code > 299):
		t.Errorf("%s: want success, got %d %s", label, code, body)
	case !allowed && code != 401 && code != 403 && code != 404:
		t.Errorf("%s: want 401/403/404, got %d %s", label, code, body)
	}
	if foreign && !allowed && (strings.Contains(string(body), e.a.id) || strings.Contains(string(body), e.a.name)) {
		t.Errorf("%s: denied response leaks team A: %s", label, body)
	}
}

func TestAuthorizationMatrix(t *testing.T) {
	e := newMatrixEnv(t)
	ops := make([]string, 0, len(matrixPolicies))
	for op := range matrixPolicies {
		ops = append(ops, op)
	}
	sort.Strings(ops)
	for _, op := range ops {
		p := matrixPolicies[op]
		t.Run(op, func(t *testing.T) {
			if p.scope == scopeGlobal {
				for who := caller(0); who < numCallers; who++ {
					e.call(t, op, p, &e.b, who, p.own.has(who), false)
				}
				return
			}
			for _, who := range []caller{cAnon, cMember, cEditor, cTeamAdmin, cOwner} {
				e.call(t, op, p, &e.b, who, p.own.has(who), false)
			}
			for who := caller(0); who < numCallers; who++ {
				e.call(t, op, p, &e.a, who, p.foreign.has(who), true)
			}
		})
	}
}
