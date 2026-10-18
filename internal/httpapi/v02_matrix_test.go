package httpapi_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/ncecere/open-mcp-portal/internal/config"
	"github.com/ncecere/open-mcp-portal/internal/testutil"
)

// Classification of the v0.2 operations in the authorization matrix
// (docs/notifications.md, admin-overview.md, automation-tokens.md): inbox and
// preferences are personal (any signed-in person); team destinations and
// tokens are the team's owners and admins (platform staff read, never write
// through team routes); platform destinations and tokens are platform admins
// (auditors read); getToken takes an automation token and nothing else, so
// every session caller is refused.

// matrixSMTP configures email against a fake server on loopback, so
// adminTestEmail has somewhere to send (its outcome is a 200 body either way).
func matrixSMTP(t *testing.T) func(*config.Config) {
	smtp := testutil.NewFakeSMTP(t)
	smtp.OfferStartTLS = false
	return func(c *config.Config) {
		c.SMTP = config.SMTP{Host: smtp.Host, Port: smtp.Port, From: "portal@example.com", TLS: "none"}
	}
}

// newDestination inserts a destination whose sealed values can't be opened:
// enough for every route, and a send or rotation with no grace never needs them.
func (e *matrixEnv) newDestination(teamID string) string {
	n := e.next()
	if teamID == "" {
		return e.exec(`INSERT INTO notification_destinations (scope, name, format, url_cipher, url_display, secret_cipher, events, team_events, created_by_name)
			VALUES ('platform', $1, 'json', '\x00', 'https://hooks.example.com/...', '\x00', '{publish.requested}', false, 'matrix') RETURNING id::text`,
			fmt.Sprintf("platform-dest-%d", n))
	}
	return e.exec(`INSERT INTO notification_destinations (scope, team_id, name, format, url_cipher, url_display, secret_cipher, events, created_by_name)
		VALUES ('team', $1, $2, 'json', '\x00', 'https://hooks.example.com/...', '\x00', '{server.unhealthy}', 'matrix') RETURNING id::text`,
		teamID, fmt.Sprintf("dest-%d", n))
}

// newToken inserts an active automation token: a platform token (teamID "")
// or a team publisher token.
func (e *matrixEnv) newToken(teamID string) string {
	n := e.next()
	if teamID == "" {
		return e.exec(`INSERT INTO automation_tokens (kind, name, prefix, digest, scopes, created_by, expires_at)
			VALUES ('platform_admin', $1::text, 'ompadm_matrix00', encode(sha256($1::text::bytea), 'hex'), '{platform:read}', $2, now() + interval '30 days') RETURNING id::text`,
			fmt.Sprintf("matrix-%d", n), e.userIDs[cPlatformAdmin])
	}
	return e.exec(`INSERT INTO automation_tokens (kind, team_id, name, prefix, digest, scopes, expires_at)
		VALUES ('team_publisher', $1, $2::text, 'omppub_matrix00', encode(sha256($2::text::bytea), 'hex'), '{servers:read}', now() + interval '30 days') RETURNING id::text`,
		teamID, fmt.Sprintf("matrix-%d", n))
}

// notification inserts an inbox row of the caller (when the call is expected
// to succeed) or of another person.
func (c *mctx) notification() string {
	owner := c.e.spareUser
	if c.allowed {
		owner = c.e.userIDs[c.who]
	}
	return c.e.exec(`INSERT INTO notification_inbox (user_id, event_id, event, title, body) VALUES ($1, $2, 'server.unhealthy', 't', 'b') RETURNING id::text`,
		owner, c.e.next())
}

func (c *mctx) teamDestination() string {
	if c.allowed {
		return c.e.newDestination(c.tf.id)
	}
	return c.e.fixture(`SELECT id::text FROM notification_destinations WHERE team_id = $1 ORDER BY created_at LIMIT 1`, func() string { return c.e.newDestination(c.tf.id) }, c.tf.id)
}

// fixture is the first row of query, or a new one when there is none.
func (e *matrixEnv) fixture(query string, make func() string, args ...any) string {
	var id string
	if err := e.app.Pool.QueryRow(context.Background(), query, args...).Scan(&id); err == nil {
		return id
	}
	return make()
}

func (c *mctx) platformDestination() string {
	if c.allowed {
		return c.e.newDestination("")
	}
	return c.e.fixture(`SELECT id::text FROM notification_destinations WHERE scope = 'platform' ORDER BY created_at LIMIT 1`, func() string { return c.e.newDestination("") })
}

func (c *mctx) teamToken() string {
	if c.allowed {
		return c.e.newToken(c.tf.id)
	}
	return c.e.fixture(`SELECT id::text FROM automation_tokens WHERE team_id = $1 ORDER BY created_at LIMIT 1`, func() string { return c.e.newToken(c.tf.id) }, c.tf.id)
}

func (c *mctx) platformToken() string {
	if c.allowed {
		return c.e.newToken("")
	}
	return c.e.fixture(`SELECT id::text FROM automation_tokens WHERE kind = 'platform_admin' ORDER BY created_at LIMIT 1`, func() string { return c.e.newToken("") })
}

type testConfig = config.Config

func put(p string, body any) request { return request{method: "PUT", path: p, body: body} }

func init() {
	const (
		urlOK     = "https://hooks.example.com/services/T000/B000/XXXX"
		teamDests = "/notification-destinations"
		adminDest = "/v1/admin/notification-destinations"
	)
	destCreate := func(c *mctx, events ...string) map[string]any {
		return map[string]any{"name": fmt.Sprintf("created-%d", c.e.next()), "format": "slack", "url": urlOK, "events": events}
	}
	for op, p := range map[string]policy{
		"adminGetOverview": {own: platform, build: func(*mctx) request { return get("/v1/admin/overview") }},
		"getTeamAttention": {scope: scopeTeam, own: members, foreign: platform, build: func(c *mctx) request { return get(c.team("/attention")) }},

		"listMyNotifications":          {own: signedIn, build: func(*mctx) request { return get("/v1/me/notifications") }},
		"getMyUnreadNotificationCount": {own: signedIn, build: func(*mctx) request { return get("/v1/me/notifications/unread-count") }},
		"markAllMyNotificationsRead":   {own: signedIn, build: func(*mctx) request { return post("/v1/me/notifications/read-all", nil) }},
		"markMyNotificationRead":       {own: signedIn, build: func(c *mctx) request { return post("/v1/me/notifications/"+c.notification()+"/read", nil) }},
		"getMyNotificationPreferences": {own: signedIn, build: func(*mctx) request { return get("/v1/me/notification-preferences") }},
		"updateMyNotificationPreferences": {own: signedIn, build: func(*mctx) request {
			return put("/v1/me/notification-preferences", map[string]any{"items": []any{map[string]any{"event": "key.expiring", "inApp": false}}})
		}},

		"listTeamNotificationDestinations": {scope: scopeTeam, own: admins, foreign: platform, build: func(c *mctx) request { return get(c.team(teamDests)) }},
		"createTeamNotificationDestination": {scope: scopeTeam, own: admins, build: func(c *mctx) request {
			return post(c.team(teamDests), destCreate(c, "server.unhealthy"))
		}},
		"getTeamNotificationDestination": {scope: scopeTeam, own: admins, foreign: platform, build: func(c *mctx) request {
			return get(c.team(teamDests + "/" + c.teamDestination()))
		}},
		"updateTeamNotificationDestination": {scope: scopeTeam, own: admins, build: func(c *mctx) request {
			return patch(c.team(teamDests+"/"+c.teamDestination()), map[string]any{"name": fmt.Sprintf("renamed-%d", c.e.next())}).h(rev1)
		}},
		"deleteTeamNotificationDestination": {scope: scopeTeam, own: admins, build: func(c *mctx) request {
			return del(c.team(teamDests + "/" + c.teamDestination()))
		}},
		"testTeamNotificationDestination": {scope: scopeTeam, own: admins, build: func(c *mctx) request {
			return post(c.team(teamDests+"/"+c.teamDestination()+"/test"), nil)
		}},
		"rotateTeamNotificationDestinationSecret": {scope: scopeTeam, own: admins, build: func(c *mctx) request {
			return post(c.team(teamDests+"/"+c.teamDestination()+"/rotate-secret"), map[string]any{"graceHours": 0})
		}},
		"listTeamNotificationDeliveries": {scope: scopeTeam, own: admins, foreign: platform, build: func(c *mctx) request {
			return get(c.team(teamDests + "/" + c.teamDestination() + "/deliveries"))
		}},

		"adminListNotificationDestinations": {own: platform, build: func(*mctx) request { return get(adminDest) }},
		"adminCreateNotificationDestination": {own: padmin, build: func(c *mctx) request {
			return post(adminDest, destCreate(c, "publish.requested"))
		}},
		"adminGetNotificationDestination": {own: platform, build: func(c *mctx) request { return get(adminDest + "/" + c.platformDestination()) }},
		"adminUpdateNotificationDestination": {own: padmin, build: func(c *mctx) request {
			return patch(adminDest+"/"+c.platformDestination(), map[string]any{"name": fmt.Sprintf("renamed-%d", c.e.next())}).h(rev1)
		}},
		"adminDeleteNotificationDestination": {own: padmin, build: func(c *mctx) request { return del(adminDest + "/" + c.platformDestination()) }},
		"adminTestNotificationDestination": {own: padmin, build: func(c *mctx) request {
			return post(adminDest+"/"+c.platformDestination()+"/test", nil)
		}},
		"adminRotateNotificationDestinationSecret": {own: padmin, build: func(c *mctx) request {
			return post(adminDest+"/"+c.platformDestination()+"/rotate-secret", map[string]any{"graceHours": 0})
		}},
		"adminListNotificationDeliveries": {own: platform, build: func(c *mctx) request {
			return get(adminDest + "/" + c.platformDestination() + "/deliveries")
		}},
		"adminGetNotificationSettings": {own: platform, build: func(*mctx) request { return get("/v1/admin/settings/notifications") }},
		"adminTestEmail":               {own: padmin, build: func(*mctx) request { return post("/v1/admin/settings/notifications/test-email", nil) }},

		"getToken":                  {build: func(*mctx) request { return get("/v1/token") }}, // token only: every session caller is refused
		"adminListAutomationTokens": {own: platform, build: func(*mctx) request { return get("/v1/admin/automation-tokens") }},
		"adminCreateAutomationToken": {own: padmin, build: func(*mctx) request {
			return post("/v1/admin/automation-tokens", map[string]any{"name": "matrix", "scopes": []string{"platform:read"}, "expiresInDays": 30})
		}},
		"adminRevokeAutomationToken": {own: padmin, build: func(c *mctx) request { return del("/v1/admin/automation-tokens/" + c.platformToken()) }},
		"listTeamAutomationTokens":   {scope: scopeTeam, own: admins, foreign: platform, build: func(c *mctx) request { return get(c.team("/automation-tokens")) }},
		"createTeamAutomationToken": {scope: scopeTeam, own: admins, build: func(c *mctx) request {
			return post(c.team("/automation-tokens"), map[string]any{"name": "matrix", "scopes": []string{"servers:write"}, "expiresInDays": 30})
		}},
		"revokeTeamAutomationToken": {scope: scopeTeam, own: admins, build: func(c *mctx) request {
			return del(c.team("/automation-tokens/" + c.teamToken()))
		}},
	} {
		if _, dup := matrixPolicies[op]; dup {
			panic("matrix: " + op + " classified twice")
		}
		matrixPolicies[op] = p
	}
}

// run executes a statement (no result).
func (e *matrixEnv) run(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.app.Pool.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
}
