package usage_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ncecere/open-mcp-gateway/internal/authz"
	"github.com/ncecere/open-mcp-gateway/internal/testutil"
	"github.com/ncecere/open-mcp-gateway/internal/usage"
)

type fixture struct {
	t    *testing.T
	pool *pgxpool.Pool
	user string
}

func (f *fixture) scalar(sql string, args ...any) string {
	f.t.Helper()
	var out string
	if err := f.pool.QueryRow(context.Background(), sql, args...).Scan(&out); err != nil {
		f.t.Fatalf("%s: %v", sql, err)
	}
	return out
}

type callSpec struct {
	at                    time.Time
	gateway, gatewayTeam  string
	server, serverTeam    string
	capability, name      string
	method, kind, outcome string
	saTeam                string
	latency               int
}

func (f *fixture) insert(c callSpec) {
	f.t.Helper()
	if c.method == "" {
		c.method = "tools/call"
	}
	if c.kind == "" {
		c.kind = "api_key"
	}
	if c.outcome == "" {
		c.outcome = "ok"
	}
	user, sa := f.user, ""
	if c.kind == "service_account" {
		user, sa = "", uuid.NewString()
	}
	f.scalar(`INSERT INTO mcp_calls (occurred_at, method, exposed_name, gateway_id, gateway_slug, gateway_name, gateway_team_id, server_id,
		server_slug, server_name, server_team_id, capability_id, principal_kind, user_id, service_account_id, service_account_team_id, outcome,
		latency_ms, request_bytes, response_bytes)
		VALUES ($1, $2, NULLIF($3, ''), $4::uuid, 'gw-' || left($4::text, 4), 'Gateway ' || left($4::text, 4), NULLIF($5, '')::uuid, NULLIF($6, '')::uuid,
		        CASE WHEN $6 <> '' THEN 'srv' END, CASE WHEN $6 <> '' THEN 'Server' END, NULLIF($7, '')::uuid, NULLIF($8, '')::uuid, $9,
		        NULLIF($10, '')::uuid, NULLIF($11, '')::uuid, NULLIF($12, '')::uuid, $13, $14, 10, 100) RETURNING id::text`,
		c.at, c.method, c.name, c.gateway, c.gatewayTeam, c.server, c.serverTeam, c.capability, c.kind, user, sa, c.saTeam, c.outcome, c.latency)
}

func TestRollupsAndReports(t *testing.T) {
	ctx := context.Background()
	pool, _ := testutil.NewDB(t)
	f := &fixture{t: t, pool: pool}
	f.user = f.scalar(`INSERT INTO users (oidc_issuer, oidc_subject, email, display_name) VALUES ('t', 's', 'a@example.com', 'A') RETURNING id::text`)
	teamA := f.scalar(`INSERT INTO teams (slug, name) VALUES ('a', 'Team A') RETURNING id::text`)
	teamB := f.scalar(`INSERT INTO teams (slug, name) VALUES ('b', 'Team B') RETURNING id::text`)
	gwA, gwP, srvB, capB := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	now := time.Now().UTC()
	prev := now.Truncate(time.Hour).Add(-time.Hour).Add(10 * time.Minute)
	// Team A's gateway calls team B's server; a platform gateway too; a
	// service account of team B through the platform gateway lists tools.
	for i, lat := range []int{3, 4, 20, 20, 400} {
		outcome := "ok"
		if i == 4 {
			outcome = "timeout"
		}
		f.insert(callSpec{at: prev, gateway: gwA, gatewayTeam: teamA, server: srvB, serverTeam: teamB, capability: capB, name: "b__tool",
			latency: lat, outcome: outcome})
	}
	f.insert(callSpec{at: prev, gateway: gwP, server: srvB, serverTeam: teamB, capability: capB, name: "b__tool", latency: 7})
	f.insert(callSpec{at: now, gateway: gwP, method: "tools/list", kind: "service_account", saTeam: teamB, latency: 1})
	f.insert(callSpec{at: now, gateway: gwP, name: "nope", outcome: "denied", latency: 1})
	s := usage.New(pool, testutil.Logger())

	res, err := s.Rollup(ctx)
	if err != nil || res.Skipped || res.Hours < 2 {
		t.Fatalf("rollup = %+v %v", res, err)
	}
	direct := f.scalar(`SELECT count(*)::text || '/' || sum(latency_ms)::text || '/' || sum(request_bytes)::text FROM mcp_calls`)
	rolled := f.scalar(`SELECT sum(calls)::text || '/' || sum(latency_sum_ms)::text || '/' || sum(request_bytes)::text FROM usage_hourly`)
	if direct != rolled {
		t.Fatalf("rollup %s != direct %s", rolled, direct)
	}
	rows := f.scalar(`SELECT count(*)::text FROM usage_hourly`)
	// Idempotent; a late commit in the previous hour is included.
	if _, err := s.Rollup(ctx); err != nil || f.scalar(`SELECT count(*)::text FROM usage_hourly`) != rows {
		t.Fatalf("re-run changed the rollups: %v", err)
	}
	f.insert(callSpec{at: prev, gateway: gwA, gatewayTeam: teamA, server: srvB, serverTeam: teamB, capability: capB, name: "b__tool", latency: 20})
	if _, err := s.Rollup(ctx); err != nil {
		t.Fatal(err)
	}
	if got := f.scalar(`SELECT sum(calls)::text FROM usage_hourly`); got != "9" {
		t.Fatalf("late call not rolled up: %s", got)
	}

	admin := authz.Actor{PlatformRole: authz.PlatformAdmin}
	rep, err := s.AdminReport(ctx, admin, usage.Query{GroupBy: usage.ByGateway})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Totals.Calls != 9 || rep.Totals.Errors != 2 || rep.Totals.Outcomes["timeout"] != 1 || rep.RolledUpTo == nil || len(rep.Rows) != 2 {
		t.Fatalf("admin by gateway = %+v rows %d", rep.Totals, len(rep.Rows))
	}
	top := rep.Rows[0]
	if top.Key != gwA || top.Metrics.Calls != 6 || top.Ref == nil || top.Ref.Exists || top.Metrics.P50 == nil {
		t.Fatalf("top row = %+v", top)
	}
	// Latencies 3, 4, 20, 20, 20, 400: the median is in (10, 25], p95 in (250, 500].
	if p50, p95 := *top.Metrics.P50, *top.Metrics.P95; p50 <= 10 || p50 > 25 || p95 <= 250 || p95 > 500 {
		t.Fatalf("percentiles = %d %d", p50, p95)
	}

	// Team scope: team B sees calls to its server and by its service
	// account; team A only its gateway's.
	ownerB := f.scalar(`INSERT INTO users (oidc_issuer, oidc_subject, email, display_name) VALUES ('t', 'b', 'b@example.com', 'B') RETURNING id::text`)
	f.scalar(`INSERT INTO team_members (team_id, user_id, role) VALUES ($1, $2, 'editor') RETURNING role`, teamB, ownerB)
	member := f.scalar(`INSERT INTO users (oidc_issuer, oidc_subject, email, display_name) VALUES ('t', 'm', 'm@example.com', 'M') RETURNING id::text`)
	f.scalar(`INSERT INTO team_members (team_id, user_id, role) VALUES ($1, $2, 'member') RETURNING role`, teamB, member)
	editor := authz.Actor{UserID: uuid.MustParse(ownerB), PlatformRole: authz.PlatformNone}
	repB, err := s.TeamReport(ctx, editor, "b", usage.Query{GroupBy: usage.ByTeam})
	if err != nil || repB.Totals.Calls != 8 {
		t.Fatalf("team B = %+v %v", repB.Totals, err)
	}
	labels := map[string]int64{}
	for _, r := range repB.Rows {
		labels[r.Label] = r.Metrics.Calls
	}
	if labels["Team A"] != 6 || labels["Platform"] != 2 {
		t.Fatalf("team B by team = %v", labels)
	}
	if _, err := s.TeamReport(ctx, authz.Actor{UserID: uuid.MustParse(member)}, "b", usage.Query{}); err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("members can't see usage: %v", err)
	}
	if _, err := s.TeamReport(ctx, authz.Actor{UserID: uuid.New()}, "b", usage.Query{}); err == nil || !strings.Contains(err.Error(), "team_not_found") {
		t.Fatalf("outsiders get 404: %v", err)
	}
	repA, _ := s.AdminReport(ctx, admin, usage.Query{Team: "a", GroupBy: usage.ByPrincipalKind})
	if repA.Totals.Calls != 6 || len(repA.Rows) != 1 || repA.Rows[0].PrincipalKind != "api_key" {
		t.Fatalf("team A by principal kind = %+v", repA)
	}
	if rep, _ := s.AdminReport(ctx, admin, usage.Query{Team: "nobody"}); rep.Totals.Calls != 0 {
		t.Fatal("an unknown team reports nothing")
	}

	// Every grouping and filter.
	gw := uuid.MustParse(gwP)
	method := "tools/list"
	kind := "service_account"
	for name, q := range map[string]usage.Query{
		"server":     {GroupBy: usage.ByServer},
		"capability": {GroupBy: usage.ByCapability, Series: true},
		"day":        {GroupBy: usage.ByDay},
		"hour":       {GroupBy: usage.ByHour},
		"filters":    {GroupBy: usage.ByGateway, Gateway: &gw, Method: &method, PrincipalKind: &kind},
	} {
		rep, err := s.AdminReport(ctx, admin, q)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		switch name {
		case "server":
			if len(rep.Rows) != 2 || rep.Rows[0].Metrics.Calls != 7 || rep.Rows[1].Key != "none" {
				t.Errorf("server = %+v", rep.Rows)
			}
		case "capability":
			if rep.Rows[0].Capability == nil || rep.Rows[0].Capability.ExposedName != "b__tool" || len(rep.Rows[0].Series) < 7 || len(rep.Rows[0].Series) > 8 {
				t.Errorf("capability = %+v", rep.Rows[0])
			}
		case "day":
			if len(rep.Rows) < 7 || rep.Rows[len(rep.Rows)-1].Metrics.Calls == 0 {
				t.Errorf("day = %d rows", len(rep.Rows))
			}
		case "hour":
			if len(rep.Rows) != 7*24 {
				t.Errorf("hour = %d rows", len(rep.Rows))
			}
		case "filters":
			if rep.Totals.Calls != 1 {
				t.Errorf("filters = %+v", rep.Totals)
			}
		}
	}
	// Bounds.
	from := now.Truncate(time.Hour).Add(-91 * 24 * time.Hour)
	if _, err := s.AdminReport(ctx, admin, usage.Query{From: &from}); err == nil || !strings.Contains(err.Error(), "invalid_range") {
		t.Fatalf("91 days: %v", err)
	}
	from = now.Truncate(time.Hour).Add(-8 * 24 * time.Hour)
	if _, err := s.AdminReport(ctx, admin, usage.Query{From: &from, GroupBy: usage.ByHour}); err == nil {
		t.Fatal("8 days by hour")
	}
	odd := now.Truncate(time.Hour).Add(-time.Minute)
	if _, err := s.AdminReport(ctx, admin, usage.Query{From: &odd}); err == nil {
		t.Fatal("from not on the hour")
	}
	if _, err := s.AdminReport(ctx, authz.Actor{}, usage.Query{}); err == nil {
		t.Fatal("people without a platform role can't read platform usage")
	}
}
