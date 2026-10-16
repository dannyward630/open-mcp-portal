package registry

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ncecere/open-mcp-gateway/internal/secrets"
	"github.com/ncecere/open-mcp-gateway/internal/store"
	"github.com/ncecere/open-mcp-gateway/internal/testutil"
	"github.com/ncecere/open-mcp-gateway/internal/upstream"
)

func testService(t *testing.T) (*Service, func(sql string, args ...any) string) {
	t.Helper()
	pool, _ := testutil.NewDB(t)
	box, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	s := New(pool, box, &upstream.Client{AllowPrivate: true, Log: testutil.Logger()}, testutil.Logger())
	exec := func(sql string, args ...any) string {
		t.Helper()
		var out string
		if err := pool.QueryRow(context.Background(), sql, args...).Scan(&out); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return out
	}
	return s, exec
}

// The health loop tests enabled servers only, records scheduled checks,
// skips servers checked recently, and runs on one replica at a time.
func TestRunHealthChecks(t *testing.T) {
	s, exec := testService(t)
	ctx := context.Background()
	fake := testutil.NewFakeMCP(t)
	team := exec(`INSERT INTO teams (slug, name) VALUES ('t', 'T') RETURNING id::text`)
	up := exec(`INSERT INTO mcp_servers (team_id, slug, name, url) VALUES ($1, 'up', 'Up', $2) RETURNING id::text`, team, fake.URL())
	down := exec(`INSERT INTO mcp_servers (team_id, slug, name, url, timeout_seconds) VALUES ($1, 'down', 'Down', 'http://127.0.0.1:1/mcp', 2)
		RETURNING id::text`, team)
	exec(`INSERT INTO mcp_servers (team_id, slug, name, url, enabled) VALUES ($1, 'off', 'Off', $2, false) RETURNING id::text`, team, fake.URL())

	sum, err := s.RunHealthChecks(ctx, 5*time.Minute)
	if err != nil || sum.Skipped || sum.Servers != 2 || sum.Healthy != 1 || sum.Failed != 1 {
		t.Fatalf("first pass = %+v, %v", sum, err)
	}
	if got := exec(`SELECT string_agg(s.slug || ':' || h.ok || ':' || h.trigger || ':' || coalesce(h.error_class, '-'), ',' ORDER BY s.slug)
		FROM mcp_health_checks h JOIN mcp_servers s ON s.id = h.server_id`); got != "down:false:scheduled:unavailable,up:true:scheduled:-" {
		t.Fatalf("checks = %s", got)
	}
	// Checked a moment ago: not due.
	if sum, err := s.RunHealthChecks(ctx, 5*time.Minute); err != nil || sum.Servers != 0 {
		t.Fatalf("second pass = %+v, %v", sum, err)
	}
	// Another replica holds the lock.
	release, ok, err := store.TryAdvisoryLockText(ctx, s.pool, healthLockKey)
	if err != nil || !ok {
		t.Fatalf("lock: %v %v", ok, err)
	}
	if sum, err := s.RunHealthChecks(ctx, time.Nanosecond); err != nil || !sum.Skipped {
		t.Fatalf("locked pass = %+v, %v", sum, err)
	}
	release()
	if sum, err := s.RunHealthChecks(ctx, time.Nanosecond); err != nil || sum.Servers != 2 {
		t.Fatalf("after release = %+v, %v", sum, err)
	}
	_ = up
	_ = down
}

// statusSince is when the current outcome began; the history keeps the
// latest KeepChecks per server.
func TestHealthStreakAndPruning(t *testing.T) {
	s, exec := testService(t)
	ctx := context.Background()
	team := exec(`INSERT INTO teams (slug, name) VALUES ('t', 'T') RETURNING id::text`)
	id := uuid.MustParse(exec(`INSERT INTO mcp_servers (team_id, slug, name, url) VALUES ($1, 's', 'S', 'https://x.example.com/mcp') RETURNING id::text`, team))
	fail := TestResult{Err: &upstream.Error{Class: upstream.ClassTimeout, Message: "slow"}}
	since := func() time.Time {
		hs, err := s.q.LatestMCPHealthChecks(ctx, []uuid.UUID{id})
		if err != nil || len(hs) != 1 {
			t.Fatalf("latest = %v %v", hs, err)
		}
		return hs[0].StatusSince
	}
	record := func(r TestResult) {
		if err := recordCheck(ctx, s.q, id, r, TriggerScheduled, uuid.NullUUID{}); err != nil {
			t.Fatal(err)
		}
	}
	record(TestResult{OK: true})
	healthySince := since()
	record(TestResult{OK: true})
	if !since().Equal(healthySince) {
		t.Fatal("a second healthy check moved statusSince")
	}
	record(fail)
	failingSince := since()
	if !failingSince.After(healthySince) {
		t.Fatal("a failure didn't start a new streak")
	}
	record(fail)
	if !since().Equal(failingSince) {
		t.Fatal("a second failure moved statusSince")
	}
	for range KeepChecks + 5 {
		record(fail)
	}
	if n := exec(`SELECT count(*)::text FROM mcp_health_checks WHERE server_id = $1`, id); n != "100" {
		t.Fatalf("kept %s checks", n)
	}
	if !since().Equal(failingSince) {
		t.Fatal("pruning lost statusSince")
	}
}
