package usage

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ncecere/open-mcp-portal/internal/apperr"
	"github.com/ncecere/open-mcp-portal/internal/authz"
	"github.com/ncecere/open-mcp-portal/internal/teams"
)

// Groupings.
const (
	ByGateway       = "gateway"
	ByServer        = "server"
	ByCapability    = "capability"
	ByTeam          = "team"
	ByPrincipalKind = "principalKind"
	ByDay           = "day"
	ByHour          = "hour"
)

// Bounds.
const (
	MaxSpan     = 90 * 24 * time.Hour
	MaxHourSpan = 7 * 24 * time.Hour
	DefaultSpan = 7 * 24 * time.Hour
	DefaultRows = 50
	MaxRows     = 200
)

// ValidGroupBy reports whether g is a grouping.
func ValidGroupBy(g string) bool {
	return slices.Contains([]string{ByGateway, ByServer, ByCapability, ByTeam, ByPrincipalKind, ByDay, ByHour}, g)
}

func timeGrouping(g string) bool { return g == ByDay || g == ByHour }

// Query is a report request.
type Query struct {
	From, To                    *time.Time
	GroupBy                     string
	Gateway, Server, Capability *uuid.UUID
	PrincipalKind, Method       *string
	Series                      bool
	Limit                       int
	// Team narrows the admin report to a team's scope (slug or ID).
	Team string
}

func invalidRange(msg string) error { return apperr.New(http.StatusBadRequest, "invalid_range", msg) }

// normalize applies defaults and bounds.
func (q *Query) normalize(now time.Time) error {
	if q.GroupBy == "" {
		q.GroupBy = ByDay
	}
	if !ValidGroupBy(q.GroupBy) {
		return apperr.Invalid("invalid_group_by", "groupBy must be gateway, server, capability, team, principalKind, day or hour")
	}
	if q.Limit == 0 {
		q.Limit = DefaultRows
	}
	if q.Limit < 1 || q.Limit > MaxRows {
		return apperr.Invalid("invalid_limit", "limit must be between 1 and 200")
	}
	to := now.UTC().Truncate(time.Hour).Add(time.Hour)
	if q.To != nil {
		to = q.To.UTC()
	}
	from := to.Add(-DefaultSpan)
	if q.From != nil {
		from = q.From.UTC()
	}
	switch {
	case !from.Equal(from.Truncate(time.Hour)) || !to.Equal(to.Truncate(time.Hour)):
		return invalidRange("from and to must be on the hour")
	case !to.After(from):
		return invalidRange("to must be after from")
	case to.Sub(from) > MaxSpan:
		return invalidRange("The range can be at most 90 days")
	case q.GroupBy == ByHour && to.Sub(from) > MaxHourSpan:
		return invalidRange("With groupBy hour the range can be at most 7 days")
	}
	q.From, q.To = &from, &to
	return nil
}

// Ref is a gateway, server or team: current slug and name while it exists,
// else as recorded.
type Ref struct {
	ID         uuid.UUID
	Slug, Name string
	Exists     bool
}

// Metrics aggregate calls.
type Metrics struct {
	Calls, Errors               int64
	Outcomes                    map[string]int64
	P50, P95                    *int32
	RequestBytes, ResponseBytes int64
	buckets                     []int64
}

func newMetrics() *Metrics {
	return &Metrics{Outcomes: map[string]int64{}, buckets: make([]int64, len(LatencyBucketsMs)+1)}
}

func (m *Metrics) add(outcome string, calls, req, resp int64, buckets []int64) {
	m.Calls += calls
	if outcome != "ok" {
		m.Errors += calls
	}
	m.Outcomes[outcome] += calls
	m.RequestBytes += req
	m.ResponseBytes += resp
	for i := range m.buckets {
		m.buckets[i] += buckets[i]
	}
}

func (m *Metrics) finish() {
	m.P50, m.P95 = Percentile(m.buckets, 0.5), Percentile(m.buckets, 0.95)
}

// Bucket is one time bucket.
type Bucket struct {
	Start   time.Time
	Metrics *Metrics
}

// Capability is a row of groupBy capability.
type Capability struct {
	ID          *uuid.UUID
	ExposedName string
	Server      *Ref
}

// Row is one group.
type Row struct {
	Key, Label    string
	Ref           *Ref
	Capability    *Capability
	PrincipalKind string
	Start         *time.Time
	Metrics       *Metrics
	Series        []Bucket
}

// Report is a usage report.
type Report struct {
	From, To   time.Time
	GroupBy    string
	RolledUpTo *time.Time
	Totals     *Metrics
	Rows       []Row
	Truncated  bool
}

var (
	errPlatformRead = apperr.Forbidden("Only platform admins and auditors can see this")
	errTeamNotFound = apperr.NotFound("team_not_found", "Team not found")
	errTeamRole     = apperr.Forbidden("Only team owners, admins and editors can see the team's usage")
)

// TeamReport reports the team's usage (owners, admins and editors;
// platform admins and auditors): calls through its gateways, to its
// servers, or by its service accounts.
func (s *Service) TeamReport(ctx context.Context, a authz.Actor, ref string, q Query) (Report, error) {
	t, err := teams.Resolve(ctx, s.q, ref)
	if err != nil {
		return Report{}, err
	}
	role, err := teams.MemberRole(ctx, s.q, t.ID, a.UserID)
	if err != nil {
		return Report{}, err
	}
	switch {
	case a.CanReadPlatform() || authz.RoleAtLeast(role, authz.RoleEditor):
	case role == "":
		return Report{}, errTeamNotFound
	default:
		return Report{}, errTeamRole
	}
	return s.report(ctx, q, &t.ID)
}

// AdminReport reports every call (platform admins and auditors); Team
// narrows to one team's scope (an unknown team reports nothing).
func (s *Service) AdminReport(ctx context.Context, a authz.Actor, q Query) (Report, error) {
	if !a.CanReadPlatform() {
		return Report{}, errPlatformRead
	}
	var team *uuid.UUID
	if q.Team != "" {
		t, err := teams.Resolve(ctx, s.q, q.Team)
		if e, ok := apperr.As(err); ok && e.Status == http.StatusNotFound {
			nobody := uuid.Nil
			team = &nobody
		} else if err != nil {
			return Report{}, err
		} else {
			team = &t.ID
		}
	}
	return s.report(ctx, q, team)
}

// grouping is how a groupBy aggregates.
type grouping struct {
	key   string // SQL expression of the key (text)
	extra []string
}

var groupings = map[string]grouping{
	ByGateway: {key: "u.gateway_id::text", extra: []string{"max(u.gateway_slug)", "max(u.gateway_name)", "''", "''", "''"}},
	ByServer: {key: "COALESCE(u.server_id::text, 'none')",
		extra: []string{"COALESCE(max(u.server_slug), '')", "COALESCE(max(u.server_name), '')", "''", "''", "''"}},
	ByCapability: {key: "COALESCE(u.capability_id::text, CASE WHEN u.exposed_name IS NOT NULL THEN 'name:' || u.exposed_name ELSE 'method:' || u.method END)",
		extra: []string{"COALESCE(max(u.exposed_name), max(u.method))", "COALESCE(max(u.server_id::text), '')", "COALESCE(max(u.server_slug), '')",
			"COALESCE(max(u.server_name), '')", "''"}},
	ByTeam:          {key: "COALESCE(u.gateway_team_id::text, 'platform')", extra: []string{"''", "''", "''", "''", "''"}},
	ByPrincipalKind: {key: "u.principal_kind", extra: []string{"''", "''", "''", "''", "''"}},
	ByDay:           {key: "to_char(date_trunc('day', u.hour, 'UTC') AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"')", extra: []string{"''", "''", "''", "''", "''"}},
	ByHour:          {key: "to_char(u.hour AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"')", extra: []string{"''", "''", "''", "''", "''"}},
}

type aggRow struct {
	key, e1, e2, e3, e4, e5 string
	day                     *time.Time
	outcome                 string
	calls, req, resp        int64
	buckets                 []int64
}

func (s *Service) query(ctx context.Context, q Query, team *uuid.UUID) ([]aggRow, error) {
	g := groupings[q.GroupBy]
	args := []any{*q.From, *q.To}
	where := []string{"u.hour >= $1", "u.hour < $2"}
	arg := func(v any) string { args = append(args, v); return fmt.Sprintf("$%d", len(args)) }
	if team != nil {
		p := arg(*team)
		where = append(where, fmt.Sprintf("(u.gateway_team_id = %[1]s OR u.server_team_id = %[1]s OR u.service_account_team_id = %[1]s)", p))
	}
	if q.Gateway != nil {
		where = append(where, "u.gateway_id = "+arg(*q.Gateway))
	}
	if q.Server != nil {
		where = append(where, "u.server_id = "+arg(*q.Server))
	}
	if q.Capability != nil {
		where = append(where, "u.capability_id = "+arg(*q.Capability))
	}
	if q.PrincipalKind != nil {
		where = append(where, "u.principal_kind = "+arg(*q.PrincipalKind))
	}
	if q.Method != nil {
		where = append(where, "u.method = "+arg(*q.Method))
	}
	day := "NULL::timestamptz"
	groupBy := "1, 8"
	if q.Series && !timeGrouping(q.GroupBy) {
		day = "date_trunc('day', u.hour, 'UTC')"
		groupBy = "1, 7, 8"
	}
	sums := make([]string, len(LatencyBucketsMs)+1)
	for i := range sums {
		sums[i] = fmt.Sprintf("COALESCE(sum(u.latency_buckets[%d]), 0)::bigint", i+1)
	}
	sql := fmt.Sprintf(`SELECT %s AS key, %s, %s, %s, %s, %s, %s AS day, u.outcome,
		sum(u.calls)::bigint, sum(u.request_bytes)::bigint, sum(u.response_bytes)::bigint, ARRAY[%s]::bigint[]
		FROM usage_hourly u WHERE %s GROUP BY %s`,
		g.key, g.extra[0], g.extra[1], g.extra[2], g.extra[3], g.extra[4], day, strings.Join(sums, ", "),
		strings.Join(where, " AND "), groupBy)
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []aggRow
	for rows.Next() {
		var r aggRow
		if err := rows.Scan(&r.key, &r.e1, &r.e2, &r.e3, &r.e4, &r.e5, &r.day, &r.outcome, &r.calls, &r.req, &r.resp, &r.buckets); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Service) report(ctx context.Context, q Query, team *uuid.UUID) (Report, error) {
	if err := q.normalize(s.now()); err != nil {
		return Report{}, err
	}
	rep := Report{From: *q.From, To: *q.To, GroupBy: q.GroupBy, Totals: newMetrics(), Rows: []Row{}}
	st, err := s.q.GetUsageRollupState(ctx)
	if err != nil {
		return Report{}, err
	}
	rep.RolledUpTo = st.RolledUpTo
	agg, err := s.query(ctx, q, team)
	if err != nil {
		return Report{}, err
	}
	byKey := map[string]*Row{}
	series := map[string]map[time.Time]*Metrics{}
	var order []string
	for _, a := range agg {
		rep.Totals.add(a.outcome, a.calls, a.req, a.resp, a.buckets)
		r, ok := byKey[a.key]
		if !ok {
			r = &Row{Key: a.key, Metrics: newMetrics()}
			describe(r, q.GroupBy, a)
			byKey[a.key] = r
			order = append(order, a.key)
		}
		r.Metrics.add(a.outcome, a.calls, a.req, a.resp, a.buckets)
		if a.day != nil {
			if series[a.key] == nil {
				series[a.key] = map[time.Time]*Metrics{}
			}
			d := a.day.UTC()
			m := series[a.key][d]
			if m == nil {
				m = newMetrics()
				series[a.key][d] = m
			}
			m.add(a.outcome, a.calls, a.req, a.resp, a.buckets)
		}
	}
	rep.Totals.finish()
	if err := s.label(ctx, q.GroupBy, byKey); err != nil {
		return Report{}, err
	}
	if timeGrouping(q.GroupBy) {
		step, start := time.Hour, *q.From
		if q.GroupBy == ByDay {
			step, start = 24*time.Hour, q.From.Truncate(24*time.Hour)
		}
		for t := start; t.Before(*q.To); t = t.Add(step) {
			key := t.Format(time.RFC3339)
			r, ok := byKey[key]
			if !ok {
				r = &Row{Key: key, Label: key, Metrics: newMetrics()}
			}
			at := t
			r.Start, r.Label = &at, key
			r.Metrics.finish()
			rep.Rows = append(rep.Rows, *r)
		}
		return rep, nil
	}
	rows := make([]Row, 0, len(order))
	for _, k := range order {
		r := byKey[k]
		r.Metrics.finish()
		if q.Series {
			for t := q.From.Truncate(24 * time.Hour); t.Before(*q.To); t = t.Add(24 * time.Hour) {
				m := series[k][t]
				if m == nil {
					m = newMetrics()
				}
				m.finish()
				r.Series = append(r.Series, Bucket{Start: t, Metrics: m})
			}
		}
		rows = append(rows, *r)
	}
	slices.SortFunc(rows, func(a, b Row) int {
		if a.Metrics.Calls != b.Metrics.Calls {
			if a.Metrics.Calls > b.Metrics.Calls {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Key, b.Key)
	})
	if len(rows) > q.Limit {
		rows, rep.Truncated = rows[:q.Limit], true
	}
	rep.Rows = rows
	return rep, nil
}

// describe fills a row's label and refs from the recorded values.
func describe(r *Row, groupBy string, a aggRow) {
	switch groupBy {
	case ByGateway:
		id, _ := uuid.Parse(a.key)
		r.Label, r.Ref = a.e2, &Ref{ID: id, Slug: a.e1, Name: a.e2}
	case ByServer:
		if a.key == "none" {
			r.Label = "No server"
			return
		}
		id, _ := uuid.Parse(a.key)
		r.Label, r.Ref = a.e2, &Ref{ID: id, Slug: a.e1, Name: a.e2}
	case ByCapability:
		c := &Capability{ExposedName: a.e1}
		if id, err := uuid.Parse(a.key); err == nil {
			c.ID = &id
		}
		if sid, err := uuid.Parse(a.e2); err == nil {
			c.Server = &Ref{ID: sid, Slug: a.e3, Name: a.e4}
		}
		r.Label, r.Capability = a.e1, c
	case ByTeam:
		if a.key == "platform" {
			r.Label = "Platform"
			return
		}
		id, _ := uuid.Parse(a.key)
		r.Label, r.Ref = a.key, &Ref{ID: id}
	case ByPrincipalKind:
		r.Label, r.PrincipalKind = a.key, a.key
	}
}

// label replaces recorded names with current ones while objects exist.
func (s *Service) label(ctx context.Context, groupBy string, rows map[string]*Row) error {
	var table string
	switch groupBy {
	case ByGateway:
		table = "gateways"
	case ByServer, ByCapability:
		table = "mcp_servers"
	case ByTeam:
		table = "teams"
	default:
		return nil
	}
	var ids []uuid.UUID
	refs := map[uuid.UUID][]*Ref{}
	for _, r := range rows {
		ref := r.Ref
		if r.Capability != nil {
			ref = r.Capability.Server
		}
		if ref == nil {
			continue
		}
		if _, ok := refs[ref.ID]; !ok {
			ids = append(ids, ref.ID)
		}
		refs[ref.ID] = append(refs[ref.ID], ref)
	}
	if len(ids) == 0 {
		return nil
	}
	res, err := s.pool.Query(ctx, "SELECT id, slug::text, name FROM "+table+" WHERE id = ANY($1)", ids)
	if err != nil {
		return err
	}
	defer res.Close()
	for res.Next() {
		var id uuid.UUID
		var slug, name string
		if err := res.Scan(&id, &slug, &name); err != nil {
			return err
		}
		for _, ref := range refs[id] {
			ref.Slug, ref.Name, ref.Exists = slug, name, true
		}
	}
	if err := res.Err(); err != nil {
		return err
	}
	for _, r := range rows {
		if r.Ref != nil && r.Capability == nil {
			if r.Ref.Name != "" {
				r.Label = r.Ref.Name
			} else if groupBy == ByTeam {
				r.Label = "Deleted team"
			}
		}
	}
	return nil
}
