// Package capture records what calls sent and received when a gateway or a
// server opts in (docs/phase6-operations.md "Payload capture", decision
// 32), and shows it to the people allowed to read it.
//
// A forwarded tools/call, prompts/get or resources/read is captured if its
// gateway's or its server's switch was on. Its arguments and result are
// redacted (sensitive member names, built-in rules, the platform admins'
// patterns, and the exact secrets the gateway handled for the call), each
// truncated to 16 KiB, sealed with ENCRYPTION_KEY and written in the same
// transaction as the call record. Never: headers, the identity JWT,
// provider tokens, client credentials or the server's static header.
//
// The team route answers the owners and admins of the team whose switch
// captured it; platform admins and auditors use the admin route. Every view
// is audited as mcp_call.payload_view.
package capture

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ncecere/open-mcp-portal/internal/apperr"
	"github.com/ncecere/open-mcp-portal/internal/audit"
	"github.com/ncecere/open-mcp-portal/internal/authz"
	"github.com/ncecere/open-mcp-portal/internal/calls"
	"github.com/ncecere/open-mcp-portal/internal/retention"
	"github.com/ncecere/open-mcp-portal/internal/secrets"
	"github.com/ncecere/open-mcp-portal/internal/store"
	"github.com/ncecere/open-mcp-portal/internal/store/dbgen"
)

// CacheTTL is how long a replica uses its copy of the admin patterns; the
// replica that saves a change applies it at once.
const CacheTTL = 30 * time.Second

// Parts.
const (
	PartArguments = "arguments"
	PartResult    = "result"
)

// AAD binds a sealed part to its call.
func AAD(callID int64, part string) []byte {
	return []byte("mcp_call_payload:" + strconv.FormatInt(callID, 10) + ":" + part)
}

var (
	errAdminOnly       = apperr.Forbidden("Only platform admins can do this")
	errPlatformRead    = apperr.Forbidden("Only platform admins and auditors can see this")
	errPayloadNotFound = apperr.NotFound("payload_not_found", "No captured contents are stored for this call that you can open")
	errTeamOnly        = apperr.Forbidden("Only the team's owners and admins can open captured contents here; platform staff use Admin › Calls")
)

func invalidPattern(e *PatternError) error {
	ae := apperr.New(http.StatusUnprocessableEntity, "invalid_pattern", e.Error())
	ae.Details = map[string]any{"index": e.Index, "reason": e.Reason}
	return ae
}

// Observer is told about captures (metrics). Every field may be nil.
type Observer struct {
	Captured   func()
	Redactions func(rule string, n int)
	Failed     func()
}

// Service manages capture settings, stores captures and reads them.
type Service struct {
	pool      *pgxpool.Pool
	q         *dbgen.Queries
	box       *secrets.Box
	calls     *calls.Service
	retention *retention.Service
	log       *slog.Logger
	Observe   Observer
	now       func() time.Time

	mu       sync.Mutex
	patterns []*regexp.Regexp
	expires  time.Time
}

// New returns a Service.
func New(pool *pgxpool.Pool, box *secrets.Box, cs *calls.Service, rs *retention.Service, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	s := &Service{pool: pool, box: box, calls: cs, retention: rs, log: log, now: time.Now}
	if pool != nil {
		s.q = dbgen.New(pool)
	}
	return s
}

// Settings are the capture settings.
type Settings struct {
	Patterns  []Pattern
	Revision  int64
	UpdatedAt time.Time
}

func decodePatterns(raw []byte) []Pattern {
	out := []Pattern{}
	_ = json.Unmarshal(raw, &out)
	return out
}

// Get returns the settings (platform admins and auditors).
func (s *Service) Get(ctx context.Context, a authz.Actor) (Settings, error) {
	if !a.CanReadPlatform() {
		return Settings{}, errPlatformRead
	}
	row, err := s.q.GetCaptureSettings(ctx)
	if err != nil {
		return Settings{}, err
	}
	return Settings{Patterns: decodePatterns(row.Patterns), Revision: row.Revision, UpdatedAt: row.UpdatedAt}, nil
}

// Update replaces the admin patterns (platform admins). 422
// invalid_pattern. Audited as settings.capture_update (patterns are not
// secrets).
func (s *Service) Update(ctx context.Context, a authz.Actor, patterns []Pattern, expectedRevision int64) (Settings, error) {
	if !a.IsPlatformAdmin() {
		return Settings{}, errAdminOnly
	}
	compiled, perr := Compile(patterns)
	if perr != nil {
		return Settings{}, invalidPattern(perr)
	}
	if patterns == nil {
		patterns = []Pattern{}
	}
	raw, err := json.Marshal(patterns)
	if err != nil {
		return Settings{}, err
	}
	var out Settings
	err = store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		cur, err := q.LockCaptureSettings(ctx)
		if err != nil {
			return err
		}
		if cur.Revision != expectedRevision {
			return apperr.Stale()
		}
		row, err := q.UpdateCapturePatterns(ctx, dbgen.UpdateCapturePatternsParams{Patterns: raw, UpdatedBy: by(a)})
		if err != nil {
			return err
		}
		out = Settings{Patterns: decodePatterns(row.Patterns), Revision: row.Revision, UpdatedAt: row.UpdatedAt}
		e := a.Audit("settings.capture_update", "platform_settings", "capture")
		e.Before, e.After = map[string]any{"patterns": decodePatterns(cur.Patterns)}, map[string]any{"patterns": out.Patterns}
		return audit.Record(ctx, q, e)
	})
	if err != nil {
		return Settings{}, err
	}
	s.setPatterns(compiled)
	return out, nil
}

func by(a authz.Actor) uuid.NullUUID {
	return uuid.NullUUID{UUID: a.UserID, Valid: a.UserID != uuid.Nil}
}

func (s *Service) setPatterns(p []*regexp.Regexp) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.patterns, s.expires = p, s.now().Add(CacheTTL)
}

// Patterns returns the compiled admin patterns, read again after CacheTTL
// (the last copy when they can't be read).
func (s *Service) Patterns(ctx context.Context) ([]*regexp.Regexp, error) {
	s.mu.Lock()
	p, fresh := s.patterns, s.now().Before(s.expires)
	s.mu.Unlock()
	if p != nil && fresh {
		return p, nil
	}
	row, err := s.q.GetCaptureSettings(ctx)
	if err != nil {
		if p != nil {
			return p, nil
		}
		return nil, err
	}
	compiled, perr := Compile(decodePatterns(row.Patterns))
	if perr != nil {
		// Stored patterns were validated; skip them rather than capture
		// nothing.
		s.log.WarnContext(ctx, "payload capture: a stored pattern is invalid", "err", perr)
		compiled = []*regexp.Regexp{}
	}
	if compiled == nil {
		compiled = []*regexp.Regexp{}
	}
	s.setPatterns(compiled)
	return compiled, nil
}

// TestResult is what a sample would become.
type TestResult struct {
	Redacted   string
	Redactions int
	Rules      map[string]int
	Truncated  bool
}

// Test shows what a sample becomes (platform admins and auditors), with
// the given patterns (nil: the saved ones). The sample is never stored,
// logged or audited.
func (s *Service) Test(ctx context.Context, a authz.Actor, sample string, patterns []Pattern) (TestResult, error) {
	if !a.CanReadPlatform() {
		return TestResult{}, errPlatformRead
	}
	var compiled []*regexp.Regexp
	if patterns != nil {
		var perr *PatternError
		if compiled, perr = Compile(patterns); perr != nil {
			return TestResult{}, invalidPattern(perr)
		}
	} else {
		row, err := s.q.GetCaptureSettings(ctx)
		if err != nil {
			return TestResult{}, err
		}
		compiled, _ = Compile(decodePatterns(row.Patterns))
	}
	p := NewRedactor(compiled).Redact([]byte(sample))
	return TestResult{Redacted: p.Content, Redactions: p.Redactions, Rules: p.Rules, Truncated: p.Truncated}, nil
}

// Input is a forwarded call to capture.
type Input struct {
	ByGateway, ByServer bool
	GatewayTeamID       uuid.NullUUID
	ServerTeamID        uuid.NullUUID
	// Arguments are the call's arguments, or {"uri": ...} for a read.
	Arguments []byte
	// Result is what the client received (nil: none).
	Result []byte
	// Secrets are exact values handled for the call (never stored).
	Secrets []string
}

// Prepared is a redacted, truncated capture waiting for its call ID.
type Prepared struct {
	in     Input
	args   Part
	result *Part
}

// Prepare redacts and truncates a capture.
func (s *Service) Prepare(ctx context.Context, in Input) (*Prepared, error) {
	patterns, err := s.Patterns(ctx)
	if err != nil {
		return nil, err
	}
	r := NewRedactor(patterns, in.Secrets...)
	p := &Prepared{in: in, args: r.Redact(in.Arguments)}
	if in.Result != nil {
		res := r.Redact(in.Result)
		p.result = &res
	}
	p.in.Arguments, p.in.Result, p.in.Secrets = nil, nil, nil
	return p, nil
}

// Hook returns the calls.Record hook that seals and stores p with its call.
func (s *Service) Hook(p *Prepared) calls.After {
	return func(ctx context.Context, q *dbgen.Queries, rec calls.Recorded) error {
		args, err := s.box.Seal([]byte(p.args.Content), AAD(rec.ID, PartArguments))
		if err != nil {
			return err
		}
		params := dbgen.InsertMCPCallPayloadParams{
			CallID: rec.ID, OccurredAt: rec.OccurredAt, GatewayTeamID: p.in.GatewayTeamID, ServerTeamID: p.in.ServerTeamID,
			ByGateway: p.in.ByGateway, ByServer: p.in.ByServer, ArgumentsCipher: args, ArgumentsBytes: p.args.Bytes,
			ArgumentsTruncated: p.args.Truncated, ArgumentsRedactions: int32(p.args.Redactions),
		}
		if p.result != nil {
			if params.ResultCipher, err = s.box.Seal([]byte(p.result.Content), AAD(rec.ID, PartResult)); err != nil {
				return err
			}
			params.ResultBytes, params.ResultTruncated, params.ResultRedactions = p.result.Bytes, p.result.Truncated, int32(p.result.Redactions)
		}
		return q.InsertMCPCallPayload(ctx, params)
	}
}

// Recorded reports the outcome of storing p (metrics and logs).
func (s *Service) Recorded(ctx context.Context, p *Prepared, captureErr error) {
	if captureErr != nil {
		s.log.WarnContext(ctx, "could not store a call's captured contents", "err", captureErr)
		if s.Observe.Failed != nil {
			s.Observe.Failed()
		}
		return
	}
	if s.Observe.Captured != nil {
		s.Observe.Captured()
	}
	if s.Observe.Redactions != nil {
		for _, part := range []*Part{&p.args, p.result} {
			if part == nil {
				continue
			}
			for rule, n := range part.Rules {
				s.Observe.Redactions(rule, n)
			}
		}
	}
}

// Failed reports a capture that couldn't be prepared.
func (s *Service) Failed(ctx context.Context, err error) {
	s.log.WarnContext(ctx, "could not capture a call's contents", "err", err)
	if s.Observe.Failed != nil {
		s.Observe.Failed()
	}
}

// PayloadPart is one opened part.
type PayloadPart struct {
	Content    string
	Bytes      int64
	Truncated  bool
	Redactions int32
}

// Payload is an opened capture.
type Payload struct {
	CallID     int64
	CapturedAt time.Time
	ExpiresAt  time.Time
	CapturedBy []string
	Arguments  PayloadPart
	Result     *PayloadPart
}

// TeamPayload opens a call's payload through a team: the call must be one
// of the team's, captured under the team's own gateway or server switch,
// and the caller one of the team's owners or admins (platform roles don't
// count here). Audited as mcp_call.payload_view (via team).
func (s *Service) TeamPayload(ctx context.Context, a authz.Actor, ref string, callID int64) (Payload, error) {
	row, sc, err := s.calls.TeamCall(ctx, a, ref, callID)
	if err != nil {
		return Payload{}, err
	}
	if !authz.RoleAtLeast(sc.Role, authz.RoleAdmin) {
		return Payload{}, errTeamOnly
	}
	if !calls.CapturedFor(row.ListMCPCallsRow, sc.Team.ID) {
		return Payload{}, errPayloadNotFound
	}
	return s.open(ctx, a, row, sc.Team.ID, "team")
}

// AdminPayload opens any call's payload (platform admins and auditors).
// Audited as mcp_call.payload_view (via admin) with the capturing team.
func (s *Service) AdminPayload(ctx context.Context, a authz.Actor, callID int64) (Payload, error) {
	row, err := s.calls.AdminCall(ctx, a, callID)
	if err != nil {
		return Payload{}, err
	}
	team := uuid.Nil
	switch {
	case row.PayloadByGateway && row.GatewayTeamID.Valid:
		team = row.GatewayTeamID.UUID
	case row.PayloadByServer && row.ServerTeamID.Valid:
		team = row.ServerTeamID.UUID
	}
	return s.open(ctx, a, row, team, "admin")
}

func (s *Service) open(ctx context.Context, a authz.Actor, row calls.Row, team uuid.UUID, via string) (Payload, error) {
	var out Payload
	err := store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		p, err := q.GetMCPCallPayload(ctx, row.ID)
		if errors.Is(store.NotFound(err), store.ErrNotFound) {
			return errPayloadNotFound
		} else if err != nil {
			return err
		}
		args, err := s.box.Open(p.ArgumentsCipher, AAD(p.CallID, PartArguments))
		if err != nil {
			return err
		}
		out = Payload{CallID: p.CallID, CapturedAt: p.CapturedAt, CapturedBy: []string{},
			Arguments: PayloadPart{Content: string(args), Bytes: p.ArgumentsBytes, Truncated: p.ArgumentsTruncated, Redactions: p.ArgumentsRedactions}}
		if p.ByGateway {
			out.CapturedBy = append(out.CapturedBy, "gateway")
		}
		if p.ByServer {
			out.CapturedBy = append(out.CapturedBy, "server")
		}
		if p.ResultCipher != nil {
			res, err := s.box.Open(p.ResultCipher, AAD(p.CallID, PartResult))
			if err != nil {
				return err
			}
			out.Result = &PayloadPart{Content: string(res), Bytes: p.ResultBytes, Truncated: p.ResultTruncated, Redactions: p.ResultRedactions}
		}
		callDays, payloadDays, err := s.retention.CallPeriods(ctx, p.GatewayTeamID, p.ServerTeamID)
		if err != nil {
			return err
		}
		out.ExpiresAt = p.OccurredAt.Add(time.Duration(min(callDays, payloadDays)) * 24 * time.Hour)
		e := a.Audit("mcp_call.payload_view", "mcp_call", strconv.FormatInt(row.ID, 10))
		e.TeamID = team
		e.Metadata = map[string]any{"via": via, "capturedBy": out.CapturedBy, "gatewayId": row.GatewayID.String()}
		return audit.Record(ctx, q, e)
	})
	return out, err
}
