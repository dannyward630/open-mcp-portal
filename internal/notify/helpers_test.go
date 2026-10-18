package notify

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ncecere/open-mcp-portal/internal/authz"
	"github.com/ncecere/open-mcp-portal/internal/config"
	"github.com/ncecere/open-mcp-portal/internal/store/dbgen"
	"github.com/ncecere/open-mcp-portal/internal/testutil"
	"github.com/ncecere/open-mcp-portal/internal/upstream"
)

// captureMailer records the mail it is asked to send.
type captureMailer struct {
	mu   sync.Mutex
	sent []Mail
	err  *MailError
}

func (m *captureMailer) Send(_ context.Context, mail Mail) *MailError {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.sent = append(m.sent, mail)
	return nil
}

func (m *captureMailer) mails() []Mail {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Mail(nil), m.sent...)
}

// syncBuffer is a log destination tests can read.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type tenv struct {
	t    *testing.T
	pool *pgxpool.Pool
	q    *dbgen.Queries
	svc  *Service
	mail *captureMailer
	logs *syncBuffer
	n    int
}

// newEnv is a service over a fresh database. client dials webhooks (nil: one
// with the development allowance, to reach httptest servers); email is on and
// captured.
func newEnv(t *testing.T, client *upstream.Client) *tenv {
	t.Helper()
	pool, _ := testutil.NewDB(t)
	if client == nil {
		client = &upstream.Client{AllowPrivate: true}
	}
	logs := &syncBuffer{}
	log := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	svc := New(pool, testBox(t), client, Config{SMTP: config.SMTP{Host: "smtp.test", From: "Portal <portal@example.com>"},
		AppURL: "https://portal.example.com", InstanceName: "Test Portal", Interval: time.Second, Version: "test"}, log)
	m := &captureMailer{}
	svc.SetMailer(m)
	return &tenv{t: t, pool: pool, q: dbgen.New(pool), svc: svc, mail: m, logs: logs}
}

func (e *tenv) exec(sql string, args ...any) string {
	e.t.Helper()
	var out string
	if err := e.pool.QueryRow(context.Background(), sql, args...).Scan(&out); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	return out
}

func (e *tenv) run(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.pool.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
}

func (e *tenv) next() int { e.n++; return e.n }

// user inserts a person (verified, active).
func (e *tenv) user(platformRole string) uuid.UUID {
	n := e.next()
	id := e.exec(`INSERT INTO users (oidc_issuer, oidc_subject, email, display_name, platform_role, email_verified)
		VALUES ('t', $1, $2, $3, $4, true) RETURNING id::text`, fmt.Sprintf("s%d", n), fmt.Sprintf("user%d@example.com", n), fmt.Sprintf("User %d", n), platformRole)
	return uuid.MustParse(id)
}

func (e *tenv) team(slug string) uuid.UUID {
	return uuid.MustParse(e.exec(`INSERT INTO teams (slug, name) VALUES ($1, $2) RETURNING id::text`, slug, "Team "+slug))
}

func (e *tenv) member(team, user uuid.UUID, role string) {
	e.run(`INSERT INTO team_members (team_id, user_id, role) VALUES ($1, $2, $3)`, team, user, role)
}

func (e *tenv) server(team uuid.UUID, state string) uuid.UUID {
	n := e.next()
	return uuid.MustParse(e.exec(`INSERT INTO mcp_servers (team_id, slug, name, url, publish_state, published_at, refreshed_at)
		VALUES ($1, $2, $3, 'https://mcp.example.com/mcp', $4::text, CASE WHEN $4::text = 'published' THEN now() END, now()) RETURNING id::text`,
		team, fmt.Sprintf("srv-%d", n), fmt.Sprintf("Server %d", n), state))
}

// check stores a health check of a server.
func (e *tenv) check(server uuid.UUID, ok bool) {
	e.t.Helper()
	p := dbgen.InsertMCPHealthCheckParams{ServerID: server, Ok: ok, Trigger: "scheduled"}
	if !ok {
		class := "unavailable"
		p.ErrorClass = &class
	}
	if _, err := e.q.InsertMCPHealthCheck(context.Background(), p); err != nil {
		e.t.Fatal(err)
	}
}

// events lists the stored events of a type: their dedup keys.
func (e *tenv) events(event string) []string {
	e.t.Helper()
	rows, err := e.pool.Query(context.Background(), `SELECT dedup_key FROM notification_events WHERE event = $1 ORDER BY id`, event)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		_ = rows.Scan(&k)
		out = append(out, k)
	}
	return out
}

// inbox counts a person's inbox rows.
func (e *tenv) inbox(user uuid.UUID) int {
	var n int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM notification_inbox WHERE user_id = $1`, user).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *tenv) count(sql string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// fanOutAll runs the dispatcher's fan-out only.
func (e *tenv) fanOutAll() {
	e.t.Helper()
	for {
		done, err := e.svc.fanOutNext(context.Background())
		if err != nil {
			e.t.Fatal(err)
		}
		if done {
			return
		}
	}
}

func (e *tenv) runOnce() RunSummary {
	e.t.Helper()
	sum, err := e.svc.Run(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	return sum
}

// emit stores a server.unhealthy-shaped event for a server of a team.
func (e *tenv) emit(typ string, team uuid.UUID, key string, data any, mutate ...func(*Event)) {
	e.t.Helper()
	ev := Event{Type: typ, DedupKey: key, TeamID: team, Data: data}
	for _, m := range mutate {
		m(&ev)
	}
	if !Emit(context.Background(), e.q, ev) {
		e.t.Fatalf("event %s was not emitted", key)
	}
}

func adminActor(id uuid.UUID) authz.Actor {
	return authz.Actor{UserID: id, PlatformRole: authz.PlatformAdmin, RequestID: "req"}
}

func personActor(id uuid.UUID) authz.Actor {
	return authz.Actor{UserID: id, PlatformRole: authz.PlatformNone, RequestID: "req"}
}
