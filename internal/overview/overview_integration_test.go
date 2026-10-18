package overview

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ncecere/open-mcp-portal/internal/authz"
	"github.com/ncecere/open-mcp-portal/internal/testutil"
)

type fx struct {
	t    *testing.T
	pool *pgxpool.Pool
	svc  *Service
	n    int
}

func newFx(t *testing.T) *fx {
	pool, _ := testutil.NewDB(t)
	return &fx{t: t, pool: pool, svc: New(pool)}
}

func (f *fx) exec(sql string, args ...any) string {
	f.t.Helper()
	var out string
	if err := f.pool.QueryRow(context.Background(), sql, args...).Scan(&out); err != nil {
		f.t.Fatalf("%s: %v", sql, err)
	}
	return out
}

func (f *fx) run(sql string, args ...any) {
	f.t.Helper()
	if _, err := f.pool.Exec(context.Background(), sql, args...); err != nil {
		f.t.Fatalf("%s: %v", sql, err)
	}
}

func (f *fx) next() int { f.n++; return f.n }

func (f *fx) user(role string) uuid.UUID {
	n := f.next()
	return uuid.MustParse(f.exec(`INSERT INTO users (oidc_issuer, oidc_subject, email, display_name, platform_role) VALUES ('t', $1, $2, $3, $4) RETURNING id::text`,
		fmt.Sprintf("s%d", n), fmt.Sprintf("u%d@example.com", n), fmt.Sprintf("U%d", n), role))
}

func (f *fx) team(slug string) uuid.UUID {
	return uuid.MustParse(f.exec(`INSERT INTO teams (slug, name) VALUES ($1::text, $1::text) RETURNING id::text`, slug))
}

func (f *fx) server(team uuid.UUID, state string, enabled bool) uuid.UUID {
	n := f.next()
	return uuid.MustParse(f.exec(`INSERT INTO mcp_servers (team_id, slug, name, url, publish_state, published_at, refreshed_at, enabled, publish_requested_at)
		VALUES ($1, $2, $3, 'https://mcp.example.com/mcp', $4::text, CASE WHEN $4::text = 'published' THEN now() END, now(), $5, CASE WHEN $4::text = 'pending' THEN now() END)
		RETURNING id::text`, team, fmt.Sprintf("s%d", n), fmt.Sprintf("Server %02d", n), state, enabled))
}

func (f *fx) check(server uuid.UUID, ok bool, since string) {
	class := "NULL"
	if !ok {
		class = "'unavailable'"
	}
	f.run(fmt.Sprintf(`INSERT INTO mcp_health_checks (server_id, ok, trigger, error_class, status_since) VALUES ($1, $2, 'scheduled', %s, now() - $3::interval)`, class), server, ok, since)
}

func admin(id uuid.UUID) authz.Actor {
	return authz.Actor{UserID: id, PlatformRole: authz.PlatformAdmin}
}

func find(items []Item, kind string) []Item {
	var out []Item
	for _, it := range items {
		if it.Kind == kind {
			out = append(out, it)
		}
	}
	return out
}

func TestAdminOverviewKinds(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	pa := f.user("platform_admin")
	team, archived := f.team("ops"), f.team("old")

	// Servers: failing published (since 30 h: critical), failing published (since 2 h: warning),
	// failing private (not in the platform view), recovered, disabled, and an archived team's.
	crit, warn := f.server(team, "published", true), f.server(team, "published", true)
	priv, recovered, disabled := f.server(team, "private", true), f.server(team, "published", true), f.server(team, "published", false)
	gone := f.server(archived, "published", true)
	f.check(crit, false, "30 hours")
	f.check(warn, false, "2 hours")
	f.check(priv, false, "5 hours")
	f.check(recovered, false, "5 hours")
	f.check(recovered, true, "1 minute")
	f.check(disabled, false, "5 hours")
	f.check(gone, false, "5 hours")
	f.run(`UPDATE teams SET status = 'archived' WHERE id = $1`, archived)

	o, err := f.svc.Admin(ctx, admin(pa))
	if err != nil {
		t.Fatal(err)
	}
	failing := find(o.Attention, ServerFailing)
	if len(failing) != 2 || o.Totals[ServerFailing] != 2 || failing[0].Severity != Critical || failing[1].Severity != Warning || failing[0].Subject.ID != crit.String() ||
		failing[0].Link == nil || failing[0].Link.Type != LinkServer || failing[0].Link.TeamSlug != "ops" || failing[0].At == nil || time.Since(*failing[0].At) < 29*time.Hour {
		t.Fatalf("failing: %+v totals %v", failing, o.Totals)
	}
	if o.Attention[0].Severity != Critical {
		t.Errorf("the most severe item comes first: %+v", o.Attention[0])
	}
	// The team's own card lists the private server too.
	tm, err := f.svc.Team(ctx, admin(pa), "ops")
	if err != nil {
		t.Fatal(err)
	}
	if got := len(find(tm.Attention, ServerFailing)); got != 3 { // crit, warn, priv (the disabled and recovered ones are out)
		t.Errorf("the team's failing servers: %d, want 3", got)
	}

	// Pending reviews: the platform's item counts servers of active teams, at = the oldest request.
	p1, p2 := f.server(team, "pending", true), f.server(team, "pending", true)
	f.server(archived, "pending", true)
	f.run(`UPDATE mcp_servers SET publish_requested_at = now() - interval '4 days' WHERE id = $1`, p1)
	f.run(`UPDATE mcp_servers SET publish_requested_at = now() - interval '1 day' WHERE id = $1`, p2)
	o, _ = f.svc.Admin(ctx, admin(pa))
	rp := find(o.Attention, ReviewPending)
	if len(rp) != 1 || *rp[0].Count != 2 || rp[0].Severity != Warning || rp[0].Link.Type != LinkReviewQueue || time.Since(*rp[0].At) < 95*time.Hour {
		t.Errorf("review pending: %+v", rp)
	}
	f.run(`UPDATE mcp_servers SET publish_requested_at = now() - interval '1 day' WHERE id = $1`, p1)
	o, _ = f.svc.Admin(ctx, admin(pa))
	if rp = find(o.Attention, ReviewPending); rp[0].Severity != Info {
		t.Errorf("a recent request is info: %+v", rp[0])
	}
	tm, _ = f.svc.Team(ctx, admin(pa), "ops")
	if rp = find(tm.Attention, ReviewPending); len(rp) != 1 || *rp[0].Count != 2 || rp[0].Link.Type != LinkServers || rp[0].Link.TeamSlug != "ops" {
		t.Errorf("the team's review item: %+v", rp)
	}

	// Changed capabilities: changed and new counted once, on published enabled servers.
	a, b := strings.Repeat("a", 64), strings.Repeat("b", 64)
	pub := f.server(team, "published", true)
	for name, approved := range map[string]*string{"changed": &b, "new": nil, "ok": &a} {
		f.run(`INSERT INTO mcp_capabilities (server_id, kind, name, definition, fingerprint, approved_fingerprint) VALUES ($1, 'tool', $2, '{}', $3, $4)`, pub, name, a, approved)
	}
	f.run(`INSERT INTO mcp_capabilities (server_id, kind, name, definition, fingerprint, approved_fingerprint) VALUES ($1, 'tool', 'private-new', '{}', $2, NULL)`, priv, a)
	o, _ = f.svc.Admin(ctx, admin(pa))
	cc := find(o.Attention, CapabilitiesChanged)
	if len(cc) != 1 || *cc[0].Count != 2 || cc[0].Severity != Warning || cc[0].Subject.ID != pub.String() {
		t.Errorf("changed capabilities: %+v", cc)
	}

	// Keys: 13, 14 and 15 days, expired and revoked ones.
	person := f.user("none")
	for _, k := range []struct {
		name, in string
		revoked  bool
	}{{"13", "13 days", false}, {"14", "13 days 23 hours", false}, {"15", "15 days", false}, {"expired", "-1 hour", false}, {"revoked", "2 days", true}} {
		f.run(`INSERT INTO api_keys (user_id, name, prefix, digest, created_at, expires_at, revoked_at) VALUES ($1, $2::text, 'omp_xxxxxxxx', encode(sha256($2::text::bytea), 'hex'),
			now() - interval '60 days', now() + $3::interval, CASE WHEN $4::bool THEN now() END)`, person, k.name, k.in, k.revoked)
	}
	o, _ = f.svc.Admin(ctx, admin(pa))
	pk := find(o.Attention, PersonalKeysExpiring)
	if len(pk) != 1 || *pk[0].Count != 2 || pk[0].Link != nil || pk[0].Severity != Info {
		t.Errorf("personal keys (13 and 14 days only): %+v", pk)
	}
	f.run(`UPDATE api_keys SET expires_at = now() + interval '2 days' WHERE name = '13'`)
	o, _ = f.svc.Admin(ctx, admin(pa))
	if pk = find(o.Attention, PersonalKeysExpiring); pk[0].Severity != Warning {
		t.Errorf("within 3 days is a warning: %+v", pk[0])
	}
	// A suspended person's keys aren't listed.
	f.run(`UPDATE users SET status = 'suspended' WHERE id = $1`, person)
	o, _ = f.svc.Admin(ctx, admin(pa))
	if len(find(o.Attention, PersonalKeysExpiring)) != 0 {
		t.Error("a suspended person's keys are listed")
	}

	// Service account keys and tokens: one item per team; platform tokens in the platform view only.
	sa := f.exec(`INSERT INTO service_accounts (team_id, name) VALUES ($1, 'bot') RETURNING id::text`, team)
	f.run(`INSERT INTO service_account_keys (service_account_id, name, prefix, digest, expires_at) VALUES ($1, 'k', 'omp_sa_xxxxxxxx', encode(sha256('a'::bytea), 'hex'), now() + interval '10 days')`, sa)
	f.run(`INSERT INTO service_account_keys (service_account_id, name, prefix, digest, expires_at) VALUES ($1, 'k2', 'omp_sa_xxxxxxxx', encode(sha256('b'::bytea), 'hex'), now() + interval '20 days')`, sa)
	f.run(`INSERT INTO automation_tokens (kind, team_id, name, prefix, digest, scopes, created_by, expires_at) VALUES ('team_publisher', $1, 'ci', 'omppub_xxxxxxxx', encode(sha256('c'::bytea), 'hex'), '{servers:read}', NULL, now() + interval '3 days')`, team)
	f.run(`INSERT INTO automation_tokens (kind, name, prefix, digest, scopes, created_by, expires_at) VALUES ('platform_admin', 'mon', 'ompadm_xxxxxxxx', encode(sha256('d'::bytea), 'hex'), '{platform:read}', $1, now() + interval '3 days')`, pa)
	o, _ = f.svc.Admin(ctx, admin(pa))
	if s := find(o.Attention, ServiceAccountKeysExpiring); len(s) != 1 || *s[0].Count != 1 || s[0].Link.Type != LinkServiceAccounts || s[0].Link.TeamSlug != "ops" {
		t.Errorf("service account keys: %+v", s)
	}
	at := find(o.Attention, AutomationTokensExpiring)
	if len(at) != 2 || at[0].Severity != Warning {
		t.Fatalf("tokens: %+v", at)
	}
	links := at[0].Link.Type + "," + at[1].Link.Type
	if links != LinkAutomationTokens+","+LinkTeamAutomationTokens && links != LinkTeamAutomationTokens+","+LinkAutomationTokens {
		t.Errorf("token links: %s", links)
	}
	if o.Counts.AutomationTokens != 2 {
		t.Errorf("automation token count: %d", o.Counts.AutomationTokens)
	}
	tm, _ = f.svc.Team(ctx, admin(pa), "ops")
	if len(find(tm.Attention, AutomationTokensExpiring)) != 1 || len(find(tm.Attention, ServiceAccountKeysExpiring)) != 1 || len(find(tm.Attention, PersonalKeysExpiring)) != 0 {
		t.Errorf("the team's key items: %+v", tm.Totals)
	}

	// Connections: a person's connection is counted; a setup sign-in isn't counted twice.
	prov := f.exec(`INSERT INTO auth_providers (slug, name, client_id, token_auth_method, pkce, authorization_url, token_url) VALUES ('gh', 'GitHub', 'c', 'none', true, 'https://a.example.com/a', 'https://a.example.com/t') RETURNING id::text`)
	active := f.user("none")
	f.run(`INSERT INTO provider_connections (user_id, provider_id, status, access_token_cipher) VALUES ($1, $2, 'needs_reconsent', NULL)`, active, prov)
	o, _ = f.svc.Admin(ctx, admin(pa))
	if c := find(o.Attention, ConnectionsReconsent); len(c) != 1 || *c[0].Count != 1 || c[0].Link.Type != LinkAuthProviders {
		t.Errorf("connections: %+v", c)
	}

	// Gateways: platform and active teams' only.
	f.run(`INSERT INTO gateways (slug, name, owner_kind, enabled) VALUES ('p-off', 'p', 'platform', false)`)
	f.run(`INSERT INTO gateways (slug, name, owner_kind, team_id, enabled) VALUES ('t-off', 't', 'team', $1, false)`, team)
	f.run(`INSERT INTO gateways (slug, name, owner_kind, team_id, enabled) VALUES ('a-off', 'a', 'team', $1, false)`, archived)
	o, _ = f.svc.Admin(ctx, admin(pa))
	if g := find(o.Attention, GatewaysDisabled); len(g) != 1 || *g[0].Count != 2 {
		t.Errorf("gateways: %+v", g)
	}

	// Notification failures: stopped or 3+ failures in a row, plus failed email.
	f.run(`INSERT INTO notification_destinations (scope, team_id, name, format, url_cipher, url_display, secret_cipher, events, consecutive_failures, last_failure_at)
		VALUES ('team', $1, 'bad', 'json', '\x00', 'https://h.example.com/...', '\x00', '{server.unhealthy}', 3, now())`, team)
	f.run(`INSERT INTO notification_destinations (scope, team_id, name, format, url_cipher, url_display, secret_cipher, events, consecutive_failures)
		VALUES ('team', $1, 'fine', 'json', '\x00', 'https://h.example.com/...', '\x00', '{server.unhealthy}', 2)`, team)
	o, _ = f.svc.Admin(ctx, admin(pa))
	if n := find(o.Attention, NotificationFailures); len(n) != 1 || *n[0].Count != 1 || n[0].Link.Type != LinkNotificationSettings {
		t.Errorf("notification failures: %+v", n)
	}
	ev := f.exec(`INSERT INTO notification_events (event, dedup_key, payload) VALUES ('key.expiring', 'x', '{}') RETURNING id::text`)
	f.run(`INSERT INTO notification_outbox (event_id, channel, user_id, state, last_attempt_at, dedup_key) VALUES ($1::bigint, 'email', $2, 'failed', now(), 'e1')`, ev, active)
	o, _ = f.svc.Admin(ctx, admin(pa))
	if n := find(o.Attention, NotificationFailures); len(n) != 1 || *n[0].Count != 2 {
		t.Errorf("failed email adds one: %+v", n)
	}
	tm, _ = f.svc.Team(ctx, admin(pa), "ops")
	if n := find(tm.Attention, NotificationFailures); len(n) != 1 || *n[0].Count != 1 || n[0].Link.Type != LinkTeamNotificationSettings {
		t.Errorf("the team's notification failures: %+v", n)
	}
}

func TestOverviewActivityMatchesTheRollups(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	pa := f.user("platform_admin")
	o, err := f.svc.Admin(ctx, admin(pa))
	if err != nil || o.Last24h.Calls != 0 || o.Last24h.ErrorRate != nil || o.Last24h.RolledUpTo != nil {
		t.Fatalf("no calls: %+v %v", o.Last24h, err)
	}
	gw := f.exec(`INSERT INTO gateways (slug, name, owner_kind) VALUES ('g', 'g', 'platform') RETURNING id::text`)
	hours := func(sub string) string { return fmt.Sprintf("date_trunc('hour', now()) - interval '%s'", sub) }
	for _, r := range []struct {
		hour, outcome string
		calls         int
	}{{"0 hours", "ok", 70}, {"5 hours", "ok", 10}, {"5 hours", "error", 5}, {"23 hours", "denied", 3}, {"23 hours", "rate_limited", 2}, {"24 hours", "ok", 500}, {"40 hours", "error", 9}} {
		f.run(fmt.Sprintf(`INSERT INTO usage_hourly (hour, gateway_id, gateway_slug, gateway_name, method, principal_kind, outcome, calls, latency_sum_ms, latency_buckets, request_bytes, response_bytes)
			VALUES (%s, $1, 'g', 'g', 'tools/call', 'api_key', $2, $3, 0, '{0}', 0, 0)`, hours(r.hour)), gw, r.outcome, r.calls)
	}
	f.run(`UPDATE usage_rollup_state SET rolled_up_to = now() - interval '2 minutes'`)
	o, _ = f.svc.Admin(ctx, admin(pa))
	l := o.Last24h
	if l.Calls != 90 || l.Errors != 10 || l.RateLimited != 2 || l.ErrorRate == nil || *l.ErrorRate < 0.111 || *l.ErrorRate > 0.112 || l.RolledUpTo == nil {
		t.Errorf("last24h: %+v", l)
	}
	if rl := find(o.Attention, RateLimited); len(rl) != 1 || *rl[0].Count != 2 || rl[0].Link.Type != LinkUsage {
		t.Errorf("rate limited: %+v", rl)
	}
	// The same figures as Admin > Usage's definition of errors: every outcome but ok.
	var want int64
	_ = f.pool.QueryRow(ctx, `SELECT COALESCE(sum(calls) FILTER (WHERE outcome <> 'ok'), 0) FROM usage_hourly WHERE hour >= date_trunc('hour', now()) - interval '23 hours'`).Scan(&want)
	if want != l.Errors {
		t.Errorf("errors %d vs the rollups' %d", l.Errors, want)
	}
}

func TestTruncationKeepsExactTotals(t *testing.T) {
	f := newFx(t)
	pa := f.user("platform_admin")
	team := f.team("ops")
	for i := 0; i < PerKind+7; i++ {
		s := f.server(team, "published", true)
		f.check(s, false, fmt.Sprintf("%d hours", i+1))
	}
	o, err := f.svc.Admin(context.Background(), admin(pa))
	if err != nil {
		t.Fatal(err)
	}
	failing := find(o.Attention, ServerFailing)
	if len(failing) != PerKind || o.Totals[ServerFailing] != PerKind+7 || !o.Truncated {
		t.Errorf("%d listed, total %d, truncated %v", len(failing), o.Totals[ServerFailing], o.Truncated)
	}
	// Oldest failure first within a severity.
	for i := 1; i < len(failing); i++ {
		if failing[i].Severity == failing[i-1].Severity && failing[i].At.Before(*failing[i-1].At) {
			t.Fatalf("not ordered by since: %d", i)
		}
	}
	// Nothing in it is a secret: summaries are names, counts and times.
	for _, it := range o.Attention {
		if strings.Contains(it.Summary, "https://") || strings.Contains(it.Summary, "omp") {
			t.Errorf("summary: %q", it.Summary)
		}
	}
}

func TestRolesAndArchivedTeams(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	team := f.team("ops")
	member, owner, outsider, auditor := f.user("none"), f.user("none"), f.user("none"), f.user("platform_auditor")
	f.run(`INSERT INTO team_members (team_id, user_id, role) VALUES ($1, $2, 'member'), ($1, $3, 'owner')`, team, member, owner)
	person := func(id uuid.UUID) authz.Actor { return authz.Actor{UserID: id, PlatformRole: authz.PlatformNone} }
	if _, err := f.svc.Admin(ctx, person(owner)); err == nil {
		t.Error("only platform staff see the platform overview")
	}
	if _, err := f.svc.Admin(ctx, authz.Actor{UserID: auditor, PlatformRole: authz.PlatformAuditor}); err != nil {
		t.Errorf("auditors do: %v", err)
	}
	if _, err := f.svc.Team(ctx, person(outsider), "ops"); err == nil || !strings.Contains(err.Error(), "team_not_found") {
		t.Errorf("an outsider: %v", err)
	}
	sa := f.exec(`INSERT INTO service_accounts (team_id, name) VALUES ($1, 'bot') RETURNING id::text`, team)
	f.run(`INSERT INTO service_account_keys (service_account_id, name, prefix, digest, expires_at) VALUES ($1, 'k', 'omp_sa_xxxxxxxx', encode(sha256('a'::bytea), 'hex'), now() + interval '5 days')`, sa)
	m, _ := f.svc.Team(ctx, person(member), "ops")
	o, _ := f.svc.Team(ctx, person(owner), "ops")
	if len(find(m.Attention, ServiceAccountKeysExpiring)) != 0 || len(find(o.Attention, ServiceAccountKeysExpiring)) != 1 {
		t.Errorf("key items are for owners and admins: member %v owner %v", m.Totals, o.Totals)
	}
	f.run(`UPDATE teams SET status = 'archived'`)
	if a, err := f.svc.Team(ctx, person(owner), "ops"); err != nil || len(a.Attention) != 0 {
		t.Errorf("archived: %+v %v", a, err)
	}
}
