// Package calls records and reads the metadata of every list and call
// through a gateway (SPEC.md §5 "Call record", docs/decisions.md decision
// 14): who, through which gateway, to which server and tool, the outcome,
// latency and sizes. Arguments and results are never stored (payload
// capture is opt-in and comes in Phase 6).
package calls

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ncecere/open-mcp-gateway/internal/apperr"
	"github.com/ncecere/open-mcp-gateway/internal/authz"
	"github.com/ncecere/open-mcp-gateway/internal/store/dbgen"
	"github.com/ncecere/open-mcp-gateway/internal/teams"
)

// Outcomes (the API's McpCallOutcome).
const (
	OutcomeOK            = "ok"
	OutcomeToolError     = "tool_error"
	OutcomeDenied        = "denied"
	OutcomeUpstreamError = "upstream_error"
	OutcomeTimeout       = "timeout"
	OutcomeTooLarge      = "too_large"
	OutcomeBadRequest    = "bad_request"
	// OutcomeNeedsAuthorization: not forwarded because the person must
	// connect (or reconnect) an account; the client got a consent link
	// (JSON-RPC -32042).
	OutcomeNeedsAuthorization = "needs_authorization"
)

// ValidOutcome reports whether o is an outcome.
func ValidOutcome(o string) bool {
	switch o {
	case OutcomeOK, OutcomeToolError, OutcomeDenied, OutcomeUpstreamError, OutcomeTimeout, OutcomeTooLarge, OutcomeBadRequest,
		OutcomeNeedsAuthorization:
		return true
	}
	return false
}

var (
	errTeamNotFound = apperr.NotFound("team_not_found", "Team not found")
	errForbidden    = apperr.Forbidden("Only team owners and admins, and platform admins and auditors, can see the team's calls")
	errPlatformRead = apperr.Forbidden("Only platform admins and auditors can see this")
)

// Principal kinds (the API's CallPrincipal.kind).
const (
	PrincipalAPIKey         = "api_key"
	PrincipalOAuth          = "oauth"
	PrincipalServiceAccount = "service_account"
)

// Call is one record. Strings are bounded by Record.
type Call struct {
	RequestID, Method, ExposedName string
	Gateway                        dbgen.Gateway
	Server                         *dbgen.McpServer
	CapabilityID                   uuid.UUID
	// PrincipalKind says who called (PrincipalAPIKey when empty): a person
	// with a key (UserID, APIKeyID) or a connected client (UserID,
	// OAuthGrantID, OAuthClientName, and OAuthClientKind: "metadata" for a
	// Client ID Metadata Document, else "registered"), or a service account
	// (ServiceAccount* and its key in APIKeyID). KeyName is the key's name as
	// it was.
	PrincipalKind                          string
	UserID, APIKeyID                       uuid.UUID
	KeyName                                string
	ServiceAccountID, ServiceAccountTeamID uuid.UUID
	ServiceAccountName                     string
	OAuthGrantID                           uuid.UUID
	OAuthClientName, OAuthClientKind       string
	ClientName                             string
	Outcome, ErrorClass                    string
	Latency                                time.Duration
	RequestBytes, ResponseBytes            int64
	// ProviderSlug is the auth provider the capability required (empty:
	// none). Never a token.
	ProviderSlug string
}

func clip(s string, n int) *string {
	if s == "" {
		return nil
	}
	r := []rune(s)
	if len(r) > n {
		r = r[:n]
	}
	out := string(r)
	return &out
}

func null(id uuid.UUID) uuid.NullUUID { return uuid.NullUUID{UUID: id, Valid: id != uuid.Nil} }

// Record stores a call.
func Record(ctx context.Context, q *dbgen.Queries, c Call) error {
	if c.PrincipalKind == "" {
		c.PrincipalKind = PrincipalAPIKey
	}
	p := dbgen.InsertMCPCallParams{
		RequestID: c.RequestID, Method: c.Method, ExposedName: clip(c.ExposedName, 1100),
		GatewayID: c.Gateway.ID, GatewaySlug: c.Gateway.Slug, GatewayName: c.Gateway.Name, GatewayTeamID: c.Gateway.TeamID,
		CapabilityID: null(c.CapabilityID), PrincipalKind: c.PrincipalKind, UserID: null(c.UserID), ApiKeyID: null(c.APIKeyID),
		KeyName: clip(c.KeyName, 100), ServiceAccountID: null(c.ServiceAccountID), ServiceAccountName: clip(c.ServiceAccountName, 64),
		ServiceAccountTeamID: null(c.ServiceAccountTeamID), OauthGrantID: null(c.OAuthGrantID), OauthClientName: clip(c.OAuthClientName, 200),
		OauthClientKind: clip(c.OAuthClientKind, 20), ClientName: clip(c.ClientName, 200), Outcome: c.Outcome, ErrorClass: clip(c.ErrorClass, 64),
		LatencyMs: int32(min(c.Latency.Milliseconds(), 1<<31-1)), RequestBytes: max(c.RequestBytes, 0), ResponseBytes: max(c.ResponseBytes, 0),
		ProviderSlug: clip(c.ProviderSlug, 48),
	}
	if c.Server != nil {
		p.ServerID, p.ServerTeamID = null(c.Server.ID), null(c.Server.TeamID)
		p.ServerSlug, p.ServerName = &c.Server.Slug, &c.Server.Name
	}
	return q.InsertMCPCall(ctx, p)
}

// Service reads call records.
type Service struct {
	pool *pgxpool.Pool
	q    *dbgen.Queries
}

// New returns a Service.
func New(pool *pgxpool.Pool) *Service {
	var q *dbgen.Queries
	if pool != nil {
		q = dbgen.New(pool)
	}
	return &Service{pool: pool, q: q}
}

// Filter narrows and pages a list (newest first, by ID).
type Filter struct {
	Gateway, Server, User, ServiceAccount *uuid.UUID
	Outcome                               *string
	From, To                              *time.Time
	BeforeID                              *int64
	Team                                  string // admin list: a team slug or ID
	Limit                                 int32
}

func (f Filter) query() dbgen.ListMCPCallsParams {
	p := dbgen.ListMCPCallsParams{Outcome: f.Outcome, OccurredFrom: f.From, OccurredTo: f.To, BeforeID: f.BeforeID, PageSize: f.Limit}
	if f.Gateway != nil {
		p.GatewayID = null(*f.Gateway)
	}
	if f.Server != nil {
		p.ServerID = null(*f.Server)
	}
	if f.User != nil {
		p.UserID = null(*f.User)
	}
	if f.ServiceAccount != nil {
		p.ServiceAccountID = null(*f.ServiceAccount)
	}
	return p
}

// ListTeam lists calls through the team's gateways, to its servers, or by
// its service accounts through any gateway (team owners and admins;
// platform admins and auditors).
func (s *Service) ListTeam(ctx context.Context, a authz.Actor, ref string, f Filter) ([]dbgen.ListMCPCallsRow, error) {
	t, err := teams.Resolve(ctx, s.q, ref)
	if err != nil {
		return nil, err
	}
	role, err := teams.MemberRole(ctx, s.q, t.ID, a.UserID)
	if err != nil {
		return nil, err
	}
	switch {
	case a.CanReadPlatform() || authz.RoleAtLeast(role, authz.RoleAdmin):
	case role == "":
		return nil, errTeamNotFound
	default:
		return nil, errForbidden
	}
	p := f.query()
	p.TeamID = null(t.ID)
	return s.q.ListMCPCalls(ctx, p)
}

// AdminList lists every call (platform admins and auditors). An unknown
// team filter lists nothing.
func (s *Service) AdminList(ctx context.Context, a authz.Actor, f Filter) ([]dbgen.ListMCPCallsRow, error) {
	if !a.CanReadPlatform() {
		return nil, errPlatformRead
	}
	p := f.query()
	if f.Team != "" {
		t, err := teams.Resolve(ctx, s.q, f.Team)
		if e, ok := apperr.As(err); ok && e.Status == 404 {
			return []dbgen.ListMCPCallsRow{}, nil
		} else if err != nil {
			return nil, err
		}
		p.TeamID = null(t.ID)
	}
	return s.q.ListMCPCalls(ctx, p)
}
