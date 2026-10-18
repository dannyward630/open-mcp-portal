package httpapi_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/ncecere/open-mcp-portal/internal/httpapi/apitypes"
)

// Automation tokens: creating (browser session only; the secret is shown
// once), listing without secrets, limits, expiry, revoking.
func TestAutomationTokenLifecycle(t *testing.T) {
	e := newMatrixEnv(t)
	admin, auditor, owner, editor := e.callers[cPlatformAdmin], e.callers[cAuditor], e.callers[cOwner], e.callers[cEditor]

	var created apitypes.AutomationTokenCreated
	code, ec := admin.call("POST", "/v1/admin/automation-tokens", map[string]any{"name": " CI monitor ", "scopes": []string{"platform:write"}}, &created, nil)
	mustCode(t, "create", code, ec, 201, "")
	tok := created.Token
	if !strings.HasPrefix(created.Secret, "ompadm_") || len(created.Secret) != len("ompadm_")+43 || tok.Prefix != created.Secret[:15] ||
		tok.Name != "CI monitor" || tok.Status != "active" || tok.Team != nil || len(tok.Scopes) != 2 || tok.Scopes[0] != "platform:read" || tok.Scopes[1] != "platform:write" {
		t.Fatalf("created: %+v secret %q", tok, created.Secret)
	}
	if days := tok.ExpiresAt.Sub(tok.CreatedAt).Hours() / 24; days < 89.9 || days > 90.1 {
		t.Errorf("the default expiry is %v days, want 90", days)
	}
	// The secret works once created, and never appears again.
	if code, _, body := e.bearer(created.Secret, get("/v1/token")); code != 200 || strings.Contains(string(body), created.Secret) {
		t.Errorf("introspection: %d %s", code, body)
	}
	for _, path := range []string{"/v1/admin/automation-tokens", "/v1/admin/audit?limit=200"} {
		for _, s := range []*session{admin, auditor} {
			_, raw := s.raw("GET", path, nil, nil)
			if strings.Contains(string(raw), created.Secret) || strings.Contains(string(raw), created.Secret[7:]) || strings.Contains(string(raw), "digest") {
				t.Errorf("%s shows the secret", path)
			}
		}
	}
	if dump := e.exec(`SELECT string_agg(a::text, ' ') FROM audit_log a`); strings.Contains(dump, created.Secret[7:]) {
		t.Error("the secret is in the audit log")
	}
	if got := e.exec(`SELECT metadata->>'kind' || '/' || (after_state->>'prefix') FROM audit_log WHERE action = 'automation_token.create'`); got != "platform_admin/"+tok.Prefix {
		t.Errorf("audit: %s", got)
	}

	// Validation.
	for name, c := range map[string]struct {
		body map[string]any
		code string
	}{
		"empty name":       {map[string]any{"name": " ", "scopes": []string{"platform:read"}}, "invalid_name"},
		"long name":        {map[string]any{"name": strings.Repeat("n", 101), "scopes": []string{"platform:read"}}, "invalid_name"},
		"newline name":     {map[string]any{"name": "a\nb", "scopes": []string{"platform:read"}}, "invalid_name"},
		"no scopes":        {map[string]any{"name": "x", "scopes": []string{}}, "invalid_scopes"},
		"team scope":       {map[string]any{"name": "x", "scopes": []string{"servers:read"}}, "invalid_scopes"},
		"mixed":            {map[string]any{"name": "x", "scopes": []string{"platform:read", "servers:read"}}, "invalid_scopes"},
		"expiry zero":      {map[string]any{"name": "x", "scopes": []string{"platform:read"}, "expiresInDays": 0}, "invalid_expiry"},
		"expiry too long":  {map[string]any{"name": "x", "scopes": []string{"platform:read"}, "expiresInDays": 366}, "invalid_expiry"},
		"expiry negative":  {map[string]any{"name": "x", "scopes": []string{"platform:read"}, "expiresInDays": -1}, "invalid_expiry"},
		"unknown scope":    {map[string]any{"name": "x", "scopes": []string{"platform:admin"}}, "invalid_scopes"},
		"unknown property": {map[string]any{"name": "x", "scopes": []string{"platform:read"}, "kind": "team_publisher"}, "invalid_json"},
	} {
		code, ec := admin.call("POST", "/v1/admin/automation-tokens", c.body, nil, nil)
		if code != 400 || (ec != c.code && !(c.code == "invalid_scopes" && ec == "invalid_json")) {
			t.Errorf("%s: %d %s, want 400 %s", name, code, ec, c.code)
		}
	}
	for _, days := range []int{1, 365} {
		var c apitypes.AutomationTokenCreated
		if code, ec := admin.call("POST", "/v1/admin/automation-tokens", map[string]any{"name": fmt.Sprintf("d%d", days), "scopes": []string{"platform:read"}, "expiresInDays": days}, &c, nil); code != 201 {
			t.Errorf("%d days: %d %s", days, code, ec)
		}
	}
	// Only platform admins make platform tokens; a team's owners and admins make theirs; editors don't.
	if code, _ := auditor.call("POST", "/v1/admin/automation-tokens", map[string]any{"name": "x", "scopes": []string{"platform:read"}}, nil, nil); code != 403 {
		t.Errorf("an auditor: %d", code)
	}
	var team apitypes.AutomationTokenCreated
	if code, ec := owner.call("POST", "/v1/teams/bravo/automation-tokens", map[string]any{"name": "ci", "scopes": []string{"servers:write"}, "expiresInDays": 30}, &team, nil); code != 201 {
		t.Fatalf("team token: %d %s", code, ec)
	}
	if !strings.HasPrefix(team.Secret, "omppub_") || team.Token.Team == nil || team.Token.Team.Slug != "bravo" || len(team.Token.Scopes) != 2 {
		t.Errorf("team token: %+v", team)
	}
	if code, _ := editor.call("POST", "/v1/teams/bravo/automation-tokens", map[string]any{"name": "x", "scopes": []string{"servers:read"}}, nil, nil); code != 403 {
		t.Errorf("an editor: %d", code)
	}
	if code, _ := e.callers[cMember].call("GET", "/v1/teams/bravo/automation-tokens", nil, nil, nil); code != 403 {
		t.Errorf("a member lists: %d", code)
	}
	// Lists: a team sees its own, the platform's list shows platform tokens only.
	var mine, plat []apitypes.AutomationToken
	owner.call("GET", "/v1/teams/bravo/automation-tokens", nil, &mine, nil)
	admin.call("GET", "/v1/admin/automation-tokens", nil, &plat, nil)
	for _, tk := range mine {
		if tk.Team == nil || tk.Team.Slug != "bravo" {
			t.Errorf("a foreign token in the team's list: %+v", tk)
		}
	}
	for _, tk := range plat {
		if tk.Team != nil || tk.Kind != "platform_admin" {
			t.Errorf("a team token in the platform list: %+v", tk)
		}
	}
	// Revoking: at once, idempotent, never across kinds or teams.
	if code, _, _ := e.bearer(team.Secret, get("/v1/teams/bravo/mcp-servers")); code != 200 {
		t.Fatal("setup")
	}
	if code, _ := owner.call("DELETE", "/v1/teams/bravo/automation-tokens/"+team.Token.Id.String(), nil, nil, nil); code != 200 {
		t.Errorf("revoke: %d", code)
	}
	if code, _, _ := e.bearer(team.Secret, get("/v1/teams/bravo/mcp-servers")); code != 401 {
		t.Errorf("a revoked token: %d", code)
	}
	if code, _ := owner.call("DELETE", "/v1/teams/bravo/automation-tokens/"+team.Token.Id.String(), nil, nil, nil); code != 200 {
		t.Errorf("revoking twice: %d", code)
	}
	if code, ec := owner.call("DELETE", "/v1/teams/bravo/automation-tokens/"+tok.Id.String(), nil, nil, nil); code != 404 || ec != "token_not_found" {
		t.Errorf("a platform token through a team route: %d %s", code, ec)
	}
	if code, ec := admin.call("DELETE", "/v1/admin/automation-tokens/"+team.Token.Id.String(), nil, nil, nil); code != 404 || ec != "token_not_found" {
		t.Errorf("a team token through the platform route: %d %s", code, ec)
	}
	otherTeam := e.newToken(e.a.id)
	if code, _ := owner.call("DELETE", "/v1/teams/bravo/automation-tokens/"+otherTeam, nil, nil, nil); code != 404 {
		t.Errorf("another team's token: %d", code)
	}
	if got := e.exec(`SELECT string_agg(action, ',' ORDER BY id) FROM audit_log WHERE action = 'automation_token.revoke'`); got != "automation_token.revoke" {
		t.Errorf("revocations audited once: %s", got)
	}
	// An archived team can't get new tokens (409) but can revoke.
	e.run(`UPDATE teams SET status = 'archived' WHERE id = $1`, e.b.id)
	if code, ec := owner.call("POST", "/v1/teams/bravo/automation-tokens", map[string]any{"name": "x", "scopes": []string{"servers:read"}}, nil, nil); code != 409 || ec != "team_archived" {
		t.Errorf("archived: %d %s", code, ec)
	}
	e.run(`UPDATE teams SET status = 'active' WHERE id = $1`, e.b.id)
}

// At most 20 active tokens per team and 20 for the platform, even when two
// creations race for the last slot.
func TestAutomationTokenLimits(t *testing.T) {
	e := newMatrixEnv(t)
	owner, admin := e.callers[cOwner], e.callers[cPlatformAdmin]
	for _, c := range []struct {
		name string
		make func(i int) (int, string)
		fill func()
	}{
		{"team", func(i int) (int, string) {
			return owner.call("POST", "/v1/teams/bravo/automation-tokens", map[string]any{"name": fmt.Sprintf("t%d", i), "scopes": []string{"servers:read"}}, nil, nil)
		}, func() {
			for i := 0; i < 19; i++ {
				e.newToken(e.b.id)
			}
		}},
		{"platform", func(i int) (int, string) {
			return admin.call("POST", "/v1/admin/automation-tokens", map[string]any{"name": fmt.Sprintf("p%d", i), "scopes": []string{"platform:read"}}, nil, nil)
		}, func() {
			for i := 0; i < 19; i++ {
				e.newToken("")
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			c.fill()
			var wg sync.WaitGroup
			codes := make([]int, 2)
			for i := range codes {
				wg.Add(1)
				go func() { defer wg.Done(); codes[i], _ = c.make(i) }()
			}
			wg.Wait()
			if !(codes[0] == 201 && codes[1] == 409 || codes[0] == 409 && codes[1] == 201) {
				t.Fatalf("two creations racing for the last slot: %v", codes)
			}
			if code, ec := c.make(9); code != 409 || ec != "too_many_tokens" {
				t.Errorf("the 21st: %d %s", code, ec)
			}
		})
	}
	// Expired and revoked tokens don't count.
	e.run(`UPDATE automation_tokens SET revoked_at = now() WHERE team_id = $1`, e.b.id)
	if code, ec := owner.call("POST", "/v1/teams/bravo/automation-tokens", map[string]any{"name": "again", "scopes": []string{"servers:read"}}, nil, nil); code != 201 {
		t.Errorf("after revoking: %d %s", code, ec)
	}
}

// Notifications through the API: inbox, preferences and webhook destinations.
func TestNotificationsAPI(t *testing.T) {
	e := newMatrixEnv(t)
	owner, member, admin, auditor := e.callers[cOwner], e.callers[cMember], e.callers[cPlatformAdmin], e.callers[cAuditor]

	// The inbox is personal: a row per person, 404 for another's.
	ownerID := e.userIDs[cOwner]
	for i := 0; i < 3; i++ {
		e.exec(`INSERT INTO notification_inbox (user_id, event_id, event, team_id, title, body, link) VALUES ($1, $2, 'server.unhealthy', $3, $4, 'b',
			jsonb_build_object('type', 'server', 'id', $5::text, 'teamSlug', 'bravo')) RETURNING id::text`, ownerID, 1000+i, e.b.id, fmt.Sprintf("title %d", i), e.b.server)
	}
	var page apitypes.NotificationPage
	if code, _ := owner.call("GET", "/v1/me/notifications?limit=2", nil, &page, nil); code != 200 || len(page.Items) != 2 || page.NextCursor == nil || page.UnreadCount != 3 ||
		page.Items[0].Title != "title 2" || page.Items[0].Link == nil || page.Items[0].Link.Type != "server" || page.Items[0].Team == nil || page.Items[0].Team.Slug != "bravo" {
		t.Fatalf("page: %+v", page)
	}
	var next apitypes.NotificationPage
	owner.call("GET", "/v1/me/notifications?limit=2&cursor="+*page.NextCursor, nil, &next, nil)
	if len(next.Items) != 1 || next.NextCursor != nil || next.Items[0].Title != "title 0" {
		t.Errorf("next page: %+v", next)
	}
	if code, ec := owner.call("GET", "/v1/me/notifications?cursor=garbage", nil, nil, nil); code != 400 || ec != "invalid_cursor" {
		t.Errorf("%d %s", code, ec)
	}
	if code, _ := owner.call("GET", "/v1/me/notifications?unread=maybe", nil, nil, nil); code != 400 {
		t.Errorf("unread=maybe: %d", code)
	}
	var cnt apitypes.NotificationUnreadCount
	owner.call("GET", "/v1/me/notifications/unread-count", nil, &cnt, nil)
	var other apitypes.NotificationUnreadCount
	member.call("GET", "/v1/me/notifications/unread-count", nil, &other, nil)
	if cnt.Count != 3 || other.Count != 0 {
		t.Errorf("counts %d / %d", cnt.Count, other.Count)
	}
	id := page.Items[0].Id
	if code, _ := member.call("POST", fmt.Sprintf("/v1/me/notifications/%d/read", id), nil, nil, nil); code != 404 {
		t.Errorf("another person's notification: %d", code)
	}
	if code, _ := owner.call("POST", fmt.Sprintf("/v1/me/notifications/%d/read", id), nil, nil, nil); code != 200 {
		t.Errorf("mark read: %d", code)
	}
	var unread apitypes.NotificationPage
	owner.call("GET", "/v1/me/notifications?unread=true", nil, &unread, nil)
	if len(unread.Items) != 2 || unread.UnreadCount != 2 {
		t.Errorf("unread: %d items, count %d", len(unread.Items), unread.UnreadCount)
	}
	var marked apitypes.NotificationMarked
	owner.call("POST", "/v1/me/notifications/read-all", nil, &marked, nil)
	if marked.Marked != 2 {
		t.Errorf("marked %d", marked.Marked)
	}

	// Preferences.
	var prefs apitypes.NotificationPreferences
	if code, _ := owner.call("GET", "/v1/me/notification-preferences", nil, &prefs, nil); code != 200 || !prefs.EmailAvailable || prefs.EmailAddress != "owner@example.com" {
		t.Fatalf("%d %+v", code, prefs)
	}
	events := func(p apitypes.NotificationPreferences) string {
		var out []string
		for _, it := range p.Items {
			out = append(out, string(it.Event))
		}
		return strings.Join(out, ",")
	}
	if got := events(prefs); strings.Contains(got, "publish.requested") || !strings.Contains(got, "publish.decided") || !strings.Contains(got, "key.expiring") {
		t.Errorf("an owner can receive: %s", got)
	}
	var adminPrefs apitypes.NotificationPreferences
	admin.call("GET", "/v1/me/notification-preferences", nil, &adminPrefs, nil)
	if got := events(adminPrefs); !strings.Contains(got, "publish.requested") || strings.Contains(got, "server.unhealthy") {
		t.Errorf("a platform admin who isn't in a team can receive: %s", got)
	}
	var updated apitypes.NotificationPreferences
	code, ec := owner.call("PUT", "/v1/me/notification-preferences", map[string]any{"items": []any{map[string]any{"event": "server.unhealthy", "email": false}}}, &updated, nil)
	if code != 200 {
		t.Fatalf("%d %s", code, ec)
	}
	for _, it := range updated.Items {
		if it.Event == "server.unhealthy" && (it.Email || !it.InApp || !it.Custom) {
			t.Errorf("%+v", it)
		}
	}
	if code, ec := owner.call("PUT", "/v1/me/notification-preferences", map[string]any{"items": []any{map[string]any{"event": "publish.requested", "inApp": false}}}, nil, nil); code != 422 || ec != "not_receivable" {
		t.Errorf("not receivable: %d %s", code, ec)
	}
	if code, _ := owner.call("PUT", "/v1/me/notification-preferences", map[string]any{"items": []any{}}, nil, nil); code != 400 {
		t.Errorf("empty update: %d", code)
	}

	// Destinations: the URL and the secret are shown once and never returned.
	const hook = "https://hooks.example.com/services/T0SENTINEL/B1SENTINEL/XSECRETSENTINEL"
	var made apitypes.NotificationDestinationCreated
	code, ec = owner.call("POST", "/v1/teams/bravo/notification-destinations", map[string]any{"name": "Ops hook", "format": "json", "url": hook,
		"events": []string{"server.unhealthy", "server.recovered"}}, &made, nil)
	mustCode(t, "create destination", code, ec, 201, "")
	d := made.Destination
	if made.Secret == nil || !strings.HasPrefix(*made.Secret, "ompwhs_") || d.UrlDisplay != "https://hooks.example.com/..." || !d.HasSecret || d.SecretHint != (*made.Secret)[len(*made.Secret)-4:] ||
		d.Scope != "team" || d.Team == nil || d.Team.Slug != "bravo" || d.Health.State != "unknown" || !d.Enabled || d.TeamEvents {
		t.Fatalf("destination: %+v secret %v", d, made.Secret)
	}
	for _, s := range []*session{owner, admin, auditor} {
		for _, path := range []string{"/v1/teams/bravo/notification-destinations", "/v1/teams/bravo/notification-destinations/" + d.Id.String(), "/v1/teams/bravo/notification-destinations/" + d.Id.String() + "/deliveries"} {
			_, raw := s.raw("GET", path, nil, nil)
			for _, bad := range []string{"SENTINEL", *made.Secret, (*made.Secret)[7:], "url_cipher", "secret_cipher"} {
				if strings.Contains(string(raw), bad) {
					t.Errorf("%s shows %q", path, bad)
				}
			}
		}
	}
	// An editor and a member can't see destinations; a non-member gets 404 from another team's path.
	if code, _ := e.callers[cEditor].call("GET", "/v1/teams/bravo/notification-destinations", nil, nil, nil); code != 403 {
		t.Errorf("editor: %d", code)
	}
	if code, ec := owner.call("GET", "/v1/teams/alpha/notification-destinations", nil, nil, nil); code != 404 || ec != "team_not_found" {
		t.Errorf("another team: %d %s", code, ec)
	}
	// URL rules, with the MCP servers' allowlist deliberately covering 10.0.0.0/8.
	e.run(`UPDATE network_settings SET entries = '[{"kind":"cidr","value":"10.0.0.0/8","allowHttp":true}]'::jsonb`)
	for url, reason := range map[string]string{"https://10.1.2.3/hook": "private_address", "http://10.1.2.3/hook": "scheme", "https://127.0.0.1/h": "blocked_address",
		"http://example.com/h": "scheme", "https://u:p@example.com/h": "userinfo", "https://example.com/h#x": "fragment", "https://169.254.169.254/": "blocked_address"} {
		var env struct {
			Error struct {
				Code    string
				Details struct{ Reason string }
			}
		}
		c, raw := owner.raw("POST", "/v1/teams/bravo/notification-destinations", map[string]any{"name": "x" + fmt.Sprint(len(url)), "format": "slack", "url": url, "events": []string{"server.unhealthy"}}, nil)
		_ = json.Unmarshal(raw, &env)
		if c != 422 || env.Error.Code != "url_not_allowed" || env.Error.Details.Reason != reason {
			t.Errorf("%s: %d %s, want 422 %s", url, c, raw, reason)
		}
	}
	// Update (If-Match), rotate, test, delete.
	if code, ec := owner.call("PATCH", "/v1/teams/bravo/notification-destinations/"+d.Id.String(), map[string]any{"name": "Ops"}, nil, nil); code != 428 {
		t.Errorf("without If-Match: %d %s", code, ec)
	}
	if code, _ := owner.call("PATCH", "/v1/teams/bravo/notification-destinations/"+d.Id.String(), map[string]any{"name": "Ops"}, nil, ifMatch(d.Revision+9)); code != 412 {
		t.Errorf("stale: %d", code)
	}
	var upd apitypes.NotificationDestination
	if code, _ := owner.call("PATCH", "/v1/teams/bravo/notification-destinations/"+d.Id.String(), map[string]any{"name": "Ops", "enabled": false}, &upd, ifMatch(d.Revision)); code != 200 || upd.Name != "Ops" || upd.Enabled || upd.Revision != d.Revision+1 {
		t.Errorf("update: %+v", upd)
	}
	var rot apitypes.NotificationDestinationSecret
	if code, _ := owner.call("POST", "/v1/teams/bravo/notification-destinations/"+d.Id.String()+"/rotate-secret", map[string]any{"graceHours": 12}, &rot, nil); code != 200 || rot.Secret == *made.Secret ||
		rot.Destination.PreviousSecretExpiresAt == nil || rot.Destination.SecretHint != rot.Secret[len(rot.Secret)-4:] {
		t.Errorf("rotate: %+v", rot.Destination)
	}
	if code, ec := owner.call("POST", "/v1/teams/bravo/notification-destinations/"+d.Id.String()+"/rotate-secret", map[string]any{"graceHours": 73}, nil, nil); code != 400 {
		t.Errorf("grace 73: %d %s", code, ec)
	}
	var slack apitypes.NotificationDestinationCreated
	owner.call("POST", "/v1/teams/bravo/notification-destinations", map[string]any{"name": "Slack", "format": "slack", "url": hook, "events": []string{"server.unhealthy"}}, &slack, nil)
	if slack.Secret != nil || slack.Destination.HasSecret {
		t.Errorf("a slack destination has no secret: %+v", slack)
	}
	if code, ec := owner.call("POST", "/v1/teams/bravo/notification-destinations/"+slack.Destination.Id.String()+"/rotate-secret", nil, nil, nil); code != 409 || ec != "not_signed" {
		t.Errorf("rotating slack: %d %s", code, ec)
	}
	if code, ec := owner.call("POST", "/v1/teams/bravo/notification-destinations", map[string]any{"name": "Ops", "format": "slack", "url": hook, "events": []string{"server.unhealthy"}}, nil, nil); code != 409 || ec != "name_taken" {
		t.Errorf("duplicate: %d %s", code, ec)
	}
	for body, want := range map[string]string{`{"name":"a","format":"xml","url":"https://x.example.com/","events":["server.unhealthy"]}`: "invalid_format", `{"name":"a","format":"slack","url":"https://x.example.com/","events":["publish.requested"]}`: "invalid_events",
		`{"name":"a","format":"slack","url":"https://x.example.com/","events":["nope"]}`: "invalid_event"} {
		var m map[string]any
		_ = json.Unmarshal([]byte(body), &m)
		if code, ec := owner.call("POST", "/v1/teams/bravo/notification-destinations", m, nil, nil); code < 400 || (ec != want && !(want == "invalid_events" && code == 422)) {
			t.Errorf("%s: %d %s", body, code, ec)
		}
	}
	if code, _ := owner.call("DELETE", "/v1/teams/bravo/notification-destinations/"+d.Id.String(), nil, nil, nil); code != 200 {
		t.Errorf("delete: %d", code)
	}
	if code, _ := owner.call("GET", "/v1/teams/bravo/notification-destinations/"+d.Id.String(), nil, nil, nil); code != 404 {
		t.Errorf("deleted: %d", code)
	}
	// Archived teams: writes are 409.
	e.run(`UPDATE teams SET status = 'archived' WHERE id = $1`, e.b.id)
	if code, ec := owner.call("POST", "/v1/teams/bravo/notification-destinations", map[string]any{"name": "arch", "format": "slack", "url": hook, "events": []string{"server.unhealthy"}}, nil, nil); code != 409 || ec != "team_archived" {
		t.Errorf("archived: %d %s", code, ec)
	}
	e.run(`UPDATE teams SET status = 'active' WHERE id = $1`, e.b.id)

	// Audit: names, events and urlDisplay; never the URL or a secret.
	dump := e.exec(`SELECT string_agg(a::text, ' ') FROM audit_log a WHERE action LIKE 'notification_destination.%'`)
	for _, bad := range []string{"SENTINEL", *made.Secret, rot.Secret} {
		if strings.Contains(dump, bad) {
			t.Errorf("the audit log contains %q", bad)
		}
	}
	for _, action := range []string{"create", "update", "rotate_secret", "delete"} {
		if !strings.Contains(dump, "notification_destination."+action) {
			t.Errorf("notification_destination.%s is not audited", action)
		}
	}

	// Settings and the test email.
	var st apitypes.NotificationSettings
	if code, _ := auditor.call("GET", "/v1/admin/settings/notifications", nil, &st, nil); code != 200 || !st.Email.Configured || st.Email.Authenticated || st.Email.Host == nil || st.Queue.IntervalSeconds != 15 {
		t.Errorf("settings: %+v", st)
	}
	_, raw := auditor.raw("GET", "/v1/admin/settings/notifications", nil, nil)
	if strings.Contains(string(raw), "password") || strings.Contains(string(raw), "username") {
		t.Errorf("settings show credentials: %s", raw)
	}
	var res apitypes.NotificationTestResult
	if code, _ := admin.call("POST", "/v1/admin/settings/notifications/test-email", nil, &res, nil); code != 200 {
		t.Errorf("test email: %d", code)
	}
}

// The overview and team attention through the API: roles, links and what a
// response may contain.
func TestOverviewAPI(t *testing.T) {
	e := newMatrixEnv(t)
	// A failing published server, a pending review and a disabled gateway.
	for i := 0; i < 3; i++ {
		e.run(`INSERT INTO mcp_health_checks (server_id, ok, trigger, error_class, message, status_since) VALUES ($1, false, 'scheduled', 'unavailable', 'secret detail', now() - interval '30 hours')`, e.b.server)
	}
	e.run(`UPDATE mcp_servers SET publish_state = 'pending', publish_requested_at = now() - interval '4 days' WHERE id = $1`, e.newServer(e.b, "private"))
	e.run(`UPDATE gateways SET enabled = false WHERE id = $1`, e.platformGateway)
	var ov apitypes.AdminOverview
	for _, who := range []caller{cPlatformAdmin, cAuditor} {
		if code, _ := e.callers[who].call("GET", "/v1/admin/overview", nil, &ov, nil); code != 200 {
			t.Fatalf("%s: %d", who, code)
		}
	}
	kinds := map[string]apitypes.AttentionItem{}
	for _, it := range ov.Attention {
		kinds[string(it.Kind)] = it
	}
	if f := kinds["server_failing"]; f.Severity != "critical" || f.Link == nil || f.Link.Type != "server" || f.Link.TeamSlug == nil || *f.Link.TeamSlug != "bravo" || f.Subject == nil || f.Team == nil {
		t.Errorf("failing: %+v", f)
	}
	if r := kinds["review_pending"]; r.Severity != "warning" || r.Count == nil || *r.Count < 1 || r.Link == nil || r.Link.Type != "review_queue" {
		t.Errorf("review: %+v", r)
	}
	if g := kinds["gateways_disabled"]; g.Link == nil || g.Link.Type != "gateways" {
		t.Errorf("gateways: %+v", g)
	}
	raw, _ := json.Marshal(ov)
	for _, bad := range []string{"secret detail", "omp_", "ompadm_", "https://"} {
		if strings.Contains(string(raw), bad) {
			t.Errorf("the overview contains %q", bad)
		}
	}
	// Team attention: members see server items, not keys or notification failures.
	e.run(`INSERT INTO service_account_keys (service_account_id, name, prefix, digest, expires_at) VALUES ($1, 'k', 'omp_sa_matrix01', encode(sha256('x'::bytea), 'hex'), now() + interval '5 days')`, e.b.serviceAccount)
	var asMember, asOwner apitypes.TeamAttention
	e.callers[cMember].call("GET", "/v1/teams/bravo/attention", nil, &asMember, nil)
	e.callers[cOwner].call("GET", "/v1/teams/bravo/attention", nil, &asOwner, nil)
	has := func(a apitypes.TeamAttention, kind string) bool {
		for _, it := range a.Attention {
			if string(it.Kind) == kind {
				return true
			}
		}
		return false
	}
	if !has(asMember, "server_failing") || has(asMember, "service_account_keys_expiring") || !has(asOwner, "service_account_keys_expiring") || !has(asOwner, "server_failing") {
		t.Errorf("member %v owner %v", asMember.Totals, asOwner.Totals)
	}
	if code, ec := e.callers[cOwner].call("GET", "/v1/teams/alpha/attention", nil, nil, nil); code != 404 || ec != "team_not_found" {
		t.Errorf("outsider: %d %s", code, ec)
	}
	e.run(`UPDATE teams SET status = 'archived' WHERE id = $1`, e.b.id)
	var arch apitypes.TeamAttention
	if code, _ := e.callers[cOwner].call("GET", "/v1/teams/bravo/attention", nil, &arch, nil); code != 200 || len(arch.Attention) != 0 || len(arch.Totals) != 0 {
		t.Errorf("an archived team: %d %+v", code, arch)
	}
	// A platform:read token reads the overview; no token reaches the team card.
	var tok string
	for _, c := range e.tokenCreds() {
		if c.name == "platform-read" {
			tok = c.secret
		}
	}
	if code, _, body := e.bearer(tok, get("/v1/admin/overview")); code != 200 {
		t.Errorf("a platform:read token: %d %s", code, body)
	}
	if code, _, body := e.bearer(tok, get("/v1/teams/bravo/attention")); code != 403 || errCode(body) != "token_not_allowed" {
		t.Errorf("a token on the team card: %d %s", code, body)
	}
}
