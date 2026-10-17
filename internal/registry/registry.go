// Package registry is the MCP server registry (SPEC.md §5, §7 phase 2;
// docs/registry.md): the remote MCP servers teams register, the tools,
// resources, resource templates and prompts discovered on them, their
// health, and publishing them to the company catalog under the platform's
// publishing policy.
//
// Header values are sealed with ENCRYPTION_KEY (internal/secrets) and never
// returned, logged or audited: only whether one is set, the header's name
// and a hint of the value's last four characters. Every mutation authorizes,
// changes and writes its audit entry in one transaction. Network exchanges
// with servers (internal/upstream) happen outside transactions.
package registry

import (
	"context"
	"errors"
	"log/slog"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ncecere/open-mcp-gateway/internal/apperr"
	"github.com/ncecere/open-mcp-gateway/internal/audit"
	"github.com/ncecere/open-mcp-gateway/internal/authz"
	"github.com/ncecere/open-mcp-gateway/internal/secrets"
	"github.com/ncecere/open-mcp-gateway/internal/store"
	"github.com/ncecere/open-mcp-gateway/internal/store/dbgen"
	"github.com/ncecere/open-mcp-gateway/internal/teams"
	"github.com/ncecere/open-mcp-gateway/internal/upstream"
)

var (
	errTeamNotFound   = apperr.NotFound("team_not_found", "Team not found")
	errNoServer       = apperr.NotFound("mcp_server_not_found", "MCP server not found")
	errNoCapability   = apperr.NotFound("mcp_capability_not_found", "Capability not found")
	errSlugTaken      = apperr.Conflict("slug_taken", "Another MCP server already uses that slug")
	errAdminOnly      = apperr.Forbidden("Only platform admins can do this")
	errPlatformRead   = apperr.Forbidden("Only platform admins and auditors can see this")
	errTeamArchived   = apperr.Conflict("team_archived", "This team is archived and read-only")
	errInvalidKind    = apperr.Invalid("invalid_kind", "kind must be tool, resource, resource_template or prompt")
	errNoteTooLong    = apperr.Invalid("invalid_note", "The note must be at most 2000 characters")
	errUndecryptable  = &upstream.Error{Class: upstream.ClassConfig, Message: "The stored header value can't be decrypted with the current ENCRYPTION_KEY. Enter it again."}
	errNotAllowedNow  = &upstream.Error{Class: upstream.ClassNotAllowed, Message: "The server's URL is not allowed by this installation's address rules."}
	slugRE            = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,46}[a-z0-9])?$`)
	headerNameRE      = regexp.MustCompile("^[A-Za-z0-9!#$%&'*+.^_`|~-]{1,100}$")
	defaultTimeoutSec = int32(30)
)

// Headers the gateway sets itself, which a server's static header may not
// replace.
var reservedHeaders = map[string]bool{
	"host": true, "content-type": true, "content-length": true, "accept": true, "connection": true,
	"transfer-encoding": true, "mcp-protocol-version": true, "mcp-session-id": true, "mcp-method": true, "mcp-name": true,
	"last-event-id": true, "user-agent": true, "cookie": true, "origin": true,
	// Gateways send the caller's identity in this header when the static
	// header is Authorization (docs/upstream-identity.md), and a person's
	// provider token with its provider's slug in these (decision 27).
	"mcp-gateway-identity": true, "mcp-gateway-provider": true, "mcp-gateway-provider-token": true,
}

// Service is the registry.
type Service struct {
	pool   *pgxpool.Pool
	q      *dbgen.Queries
	box    *secrets.Box
	client *upstream.Client
	nets   *Networks
	log    *slog.Logger
}

// New returns a Service. client reaches the servers (its AllowPrivate is the
// development allowance, which also applies to URLs when saved). Its
// Networks should be a *Networks (NewNetworks), whose admin entries this
// service edits; when it has none, one without deployment entries is set.
func New(pool *pgxpool.Pool, box *secrets.Box, client *upstream.Client, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	nets, _ := client.Networks.(*Networks)
	if nets == nil {
		nets = NewNetworks(pool, nil, log)
		if client.Networks == nil {
			client.Networks = nets
		}
	}
	return &Service{pool: pool, q: dbgen.New(pool), box: box, client: client, nets: nets, log: log}
}

// ServerAAD binds a server's sealed header value to its row.
func ServerAAD(id uuid.UUID) []byte { return []byte("mcp_server:" + id.String()) }

func by(a authz.Actor) uuid.NullUUID {
	return uuid.NullUUID{UUID: a.UserID, Valid: a.UserID != uuid.Nil}
}

// teamAccess is a team as seen by an actor.
type teamAccess struct {
	team dbgen.Team
	role string // "" when not a member
}

// access resolves a team the actor may see: members, and platform admins
// and auditors. Others get 404, so a team's existence is not revealed.
func access(ctx context.Context, q *dbgen.Queries, a authz.Actor, ref string) (teamAccess, error) {
	t, err := teams.Resolve(ctx, q, ref)
	if err != nil {
		return teamAccess{}, err
	}
	role, err := teams.MemberRole(ctx, q, t.ID, a.UserID)
	if err != nil {
		return teamAccess{}, err
	}
	if role == "" && !a.CanReadPlatform() {
		return teamAccess{}, errTeamNotFound
	}
	return teamAccess{team: t, role: role}, nil
}

// writable checks a team role rule and that the team is active.
func (ta teamAccess) writable(rule func(string) error) error {
	if err := rule(ta.role); err != nil {
		return err
	}
	if ta.team.Status != teams.StatusActive {
		return errTeamArchived
	}
	return nil
}

// teamServer returns one of the team's servers (404 for another team's).
func teamServer(ctx context.Context, q *dbgen.Queries, teamID, id uuid.UUID, lock bool) (dbgen.McpServer, error) {
	var (
		sv  dbgen.McpServer
		err error
	)
	if lock {
		sv, err = q.LockMCPServer(ctx, id)
	} else {
		sv, err = q.GetMCPServer(ctx, id)
	}
	if errors.Is(store.NotFound(err), store.ErrNotFound) || (err == nil && sv.TeamID != teamID) {
		return sv, errNoServer
	}
	return sv, err
}

func anyServer(ctx context.Context, q *dbgen.Queries, id uuid.UUID, lock bool) (dbgen.McpServer, error) {
	var (
		sv  dbgen.McpServer
		err error
	)
	if lock {
		sv, err = q.LockMCPServer(ctx, id)
	} else {
		sv, err = q.GetMCPServer(ctx, id)
	}
	if errors.Is(store.NotFound(err), store.ErrNotFound) {
		return sv, errNoServer
	}
	return sv, err
}

// Counts are capabilities per kind.
type Counts struct {
	Tools, Resources, ResourceTemplates, Prompts int64
}

func (c *Counts) add(kind string, n int64) {
	switch kind {
	case upstream.KindTool:
		c.Tools += n
	case upstream.KindResource:
		c.Resources += n
	case upstream.KindResourceTemplate:
		c.ResourceTemplates += n
	case upstream.KindPrompt:
		c.Prompts += n
	}
}

// ServerView is a server as the API shows it.
type ServerView struct {
	Server          dbgen.McpServer
	TeamSlug        string
	TeamName        string
	TeamStatus      string // its team's: archived teams' servers are out of the catalog and gateways
	EffectivePolicy string
	ReviewedByName  string
	// Counts are listed (not gone) capabilities; Approved the approved ones.
	Counts, Approved Counts
	// PendingChanges counts listed capabilities of a published server whose
	// current definition is not approved: changed since approval, or new.
	PendingChanges int64
	// Health is the latest check (nil before the first).
	Health *dbgen.McpHealthCheck
	// Requirement is the server's user-authorization requirement (nil: none).
	Requirement *ServerRequirement
}

// HasAuth reports whether the server has a stored header.
func (v ServerView) HasAuth() bool { return v.Server.AuthValueCipher != nil }

// views completes listed rows with counts, health and the effective policy.
func views(ctx context.Context, q *dbgen.Queries, rows []dbgen.ListMCPServersRow) ([]ServerView, error) {
	out := make([]ServerView, len(rows))
	if len(rows) == 0 {
		return out, nil
	}
	settings, err := q.GetPlatformSettings(ctx)
	if err != nil {
		return nil, err
	}
	ps, err := LoadProviders(ctx, q)
	if err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, len(rows))
	index := make(map[uuid.UUID]int, len(rows))
	for i, r := range rows {
		ids[i], index[r.McpServer.ID] = r.McpServer.ID, i
		out[i] = ServerView{
			Server: r.McpServer, TeamSlug: r.TeamSlug, TeamName: r.TeamName, TeamStatus: r.TeamStatus, ReviewedByName: r.ReviewedByName,
			EffectivePolicy: authz.EffectivePolicy(r.TeamPolicyOverride, settings.PublishDefaultPolicy),
			Requirement:     RequirementOf(r.McpServer, ps),
		}
	}
	stats, err := q.MCPCapabilityStats(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, st := range stats {
		v := &out[index[st.ServerID]]
		v.Counts.add(st.Kind, st.Listed)
		v.Approved.add(st.Kind, st.Approved)
		if v.Server.PublishState == StatePublished {
			v.PendingChanges += st.Unapproved
		}
	}
	health, err := q.LatestMCPHealthChecks(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i := range health {
		out[index[health[i].ServerID]].Health = &health[i]
	}
	return out, nil
}

func (s *Service) view(ctx context.Context, q *dbgen.Queries, id uuid.UUID) (ServerView, error) {
	r, err := q.GetMCPServerView(ctx, id)
	if errors.Is(store.NotFound(err), store.ErrNotFound) {
		return ServerView{}, errNoServer
	} else if err != nil {
		return ServerView{}, err
	}
	vs, err := views(ctx, q, []dbgen.ListMCPServersRow{{McpServer: r.McpServer, TeamSlug: r.TeamSlug, TeamName: r.TeamName,
		TeamPolicyOverride: r.TeamPolicyOverride, TeamStatus: r.TeamStatus, ReviewedByName: r.ReviewedByName}})
	if err != nil {
		return ServerView{}, err
	}
	return vs[0], nil
}

// ListParams filter and page server lists by (name, id).
type ListParams struct {
	Search       *string // LIKE-escaped by the service
	PublishState *string
	ChangesOnly  bool
	Team         string // admin list: a team slug or ID ("" for all)
	// ProviderID narrows to servers that require that auth provider.
	ProviderID *uuid.UUID
	AfterName  *string
	AfterID    *uuid.UUID
	Limit      int32
}

func (p ListParams) query() dbgen.ListMCPServersParams {
	out := dbgen.ListMCPServersParams{PublishState: p.PublishState, ChangesOnly: p.ChangesOnly, AfterName: p.AfterName, PageSize: p.Limit}
	if p.Search != nil {
		escaped := store.EscapeLike(*p.Search)
		out.Search = &escaped
	}
	if p.AfterID != nil {
		out.AfterID = uuid.NullUUID{UUID: *p.AfterID, Valid: true}
	}
	if p.ProviderID != nil {
		out.ProviderID = uuid.NullUUID{UUID: *p.ProviderID, Valid: true}
	}
	return out
}

// ListTeam lists a team's servers (members; platform admins and auditors).
func (s *Service) ListTeam(ctx context.Context, a authz.Actor, ref string, p ListParams) ([]ServerView, error) {
	ta, err := access(ctx, s.q, a, ref)
	if err != nil {
		return nil, err
	}
	qp := p.query()
	qp.TeamID = uuid.NullUUID{UUID: ta.team.ID, Valid: true}
	rows, err := s.q.ListMCPServers(ctx, qp)
	if err != nil {
		return nil, err
	}
	return views(ctx, s.q, rows)
}

// GetTeam returns one of a team's servers.
func (s *Service) GetTeam(ctx context.Context, a authz.Actor, ref string, id uuid.UUID) (ServerView, error) {
	ta, err := access(ctx, s.q, a, ref)
	if err != nil {
		return ServerView{}, err
	}
	if _, err := teamServer(ctx, s.q, ta.team.ID, id, false); err != nil {
		return ServerView{}, err
	}
	return s.view(ctx, s.q, id)
}

// AdminList lists every team's servers (platform admins and auditors). An
// unknown team filter lists nothing.
func (s *Service) AdminList(ctx context.Context, a authz.Actor, p ListParams) ([]ServerView, error) {
	if !a.CanReadPlatform() {
		return nil, errPlatformRead
	}
	qp := p.query()
	if p.Team != "" {
		t, err := teams.Resolve(ctx, s.q, p.Team)
		if errors.Is(err, errTeamNotFound) || isNotFound(err) {
			return []ServerView{}, nil
		} else if err != nil {
			return nil, err
		}
		qp.TeamID = uuid.NullUUID{UUID: t.ID, Valid: true}
	}
	rows, err := s.q.ListMCPServers(ctx, qp)
	if err != nil {
		return nil, err
	}
	return views(ctx, s.q, rows)
}

func isNotFound(err error) bool {
	e, ok := apperr.As(err)
	return ok && e.Status == 404
}

// AdminGet returns any server (platform admins and auditors).
func (s *Service) AdminGet(ctx context.Context, a authz.Actor, id uuid.UUID) (ServerView, error) {
	if !a.CanReadPlatform() {
		return ServerView{}, errPlatformRead
	}
	return s.view(ctx, s.q, id)
}

// CreateInput registers a server. AuthHeaderName and AuthValue are both
// empty (no credentials) or both set.
type CreateInput struct {
	Slug, Name, Description, URL string
	AuthHeaderName, AuthValue    string
	TimeoutSeconds               int32 // 0: the default (30)
	Enabled                      bool
	// Requirement is the user-authorization requirement (nil: none).
	Requirement *RequirementInput
}

// UpdateInput holds optional changes. AuthHeaderName "" removes the header
// and its value; AuthValue nil keeps the stored value.
type UpdateInput struct {
	Name, Description, URL    *string
	AuthHeaderName, AuthValue *string
	TimeoutSeconds            *int32
	Enabled                   *bool
	// SetRequirement changes the requirement to Requirement (nil removes
	// it); false keeps it.
	SetRequirement bool
	Requirement    *RequirementInput
}

func checkFields(name, description string, timeout int32) error {
	if n := len([]rune(strings.TrimSpace(name))); n < 1 || n > 100 {
		return apperr.Invalid("invalid_name", "Name must be 1-100 characters")
	}
	if len([]rune(description)) > 2000 {
		return apperr.Invalid("invalid_description", "Description must be at most 2000 characters")
	}
	if timeout < 1 || timeout > 120 {
		return apperr.Invalid("invalid_timeout", "Timeout must be between 1 and 120 seconds")
	}
	return nil
}

func checkHeader(name, value string) error {
	if !headerNameRE.MatchString(name) || reservedHeaders[strings.ToLower(name)] {
		return apperr.Invalid("invalid_auth_header", "The header name must be a valid HTTP header such as Authorization or X-API-Key")
	}
	if value == "" || len(value) > 4096 || strings.ContainsAny(value, "\r\n\x00") {
		return apperr.Invalid("invalid_auth_value", "The header value must be 1-4096 characters on one line")
	}
	return nil
}

// valueHint is the last four characters of a value of at least eight.
func valueHint(v string) string {
	r := []rune(v)
	if len(r) < 8 {
		return ""
	}
	return string(r[len(r)-4:])
}

// snapshot is a server's audited configuration: never the header value or
// its ciphertext, only whether one is set.
func snapshot(s dbgen.McpServer) map[string]any {
	out := map[string]any{"slug": s.Slug, "name": s.Name, "description": s.Description, "url": s.URL,
		"timeoutSeconds": s.TimeoutSeconds, "enabled": s.Enabled, "hasAuth": s.AuthValueCipher != nil,
		"publishState": s.PublishState, "requirement": requirementSnapshot(s)}
	if s.AuthHeaderName != nil {
		out["authHeaderName"] = *s.AuthHeaderName
	}
	return out
}

// Created is the outcome of registering a server: the server, and its
// first discovery (Refresh when it worked, Failure when not).
type Created struct {
	View    ServerView
	Refresh *RefreshResult
	Failure *TestResult
}

// Create registers a server (team editors and above) and reads its
// capabilities at once. The server is created even when that fails.
// Audited as mcp_server.create (and mcp_server.refresh when discovery works).
func (s *Service) Create(ctx context.Context, a authz.Actor, ref string, in CreateInput) (Created, error) {
	in.Slug = strings.TrimSpace(in.Slug)
	in.Name = strings.TrimSpace(in.Name)
	if in.TimeoutSeconds == 0 {
		in.TimeoutSeconds = defaultTimeoutSec
	}
	id := uuid.New()
	err := store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		ta, err := access(ctx, q, a, ref)
		if err != nil {
			return err
		}
		if err := ta.writable(authz.CheckServerEdit); err != nil {
			return err
		}
		p, err := s.insertParams(ctx, id, ta.team.ID, a, in)
		if err != nil {
			return err
		}
		if p.AuthProviderID, p.AuthScopes, err = checkRequirement(ctx, q, in.Requirement, uuid.NullUUID{}); err != nil {
			return err
		}
		row, err := q.InsertMCPServer(ctx, p)
		if apperr.IsUniqueViolation(err, "mcp_servers_slug_key") {
			return errSlugTaken
		} else if err != nil {
			return err
		}
		e := a.Audit("mcp_server.create", "mcp_server", id.String())
		e.TeamID, e.After = ta.team.ID, snapshot(row)
		return audit.Record(ctx, q, e)
	})
	if err != nil {
		return Created{}, err
	}
	sv, err := s.q.GetMCPServer(ctx, id)
	if err != nil {
		return Created{}, err
	}
	var out Created
	res, fail, err := s.refresh(ctx, a, sv)
	if err != nil {
		return Created{}, err
	}
	out.Refresh, out.Failure = res, fail
	out.View, err = s.view(ctx, s.q, id)
	return out, err
}

func (s *Service) insertParams(ctx context.Context, id, teamID uuid.UUID, a authz.Actor, in CreateInput) (dbgen.InsertMCPServerParams, error) {
	if !slugRE.MatchString(in.Slug) {
		return dbgen.InsertMCPServerParams{}, apperr.Invalid("invalid_slug",
			"Slug must be 1-48 lowercase letters, digits or hyphens, and cannot start or end with a hyphen")
	}
	if err := checkFields(in.Name, in.Description, in.TimeoutSeconds); err != nil {
		return dbgen.InsertMCPServerParams{}, err
	}
	u, err := s.client.NormalizeURL(ctx, in.URL)
	if err != nil {
		return dbgen.InsertMCPServerParams{}, err
	}
	p := dbgen.InsertMCPServerParams{ID: id, TeamID: teamID, Slug: in.Slug, Name: in.Name, Description: in.Description, URL: u,
		TimeoutSeconds: in.TimeoutSeconds, Enabled: in.Enabled, CreatedBy: by(a), AuthScopes: []string{}}
	if in.AuthHeaderName != "" || in.AuthValue != "" {
		if err := checkHeader(in.AuthHeaderName, in.AuthValue); err != nil {
			return p, err
		}
		if p.AuthValueCipher, err = s.box.Seal([]byte(in.AuthValue), ServerAAD(id)); err != nil {
			return p, err
		}
		name := in.AuthHeaderName
		p.AuthHeaderName, p.AuthValueHint = &name, valueHint(in.AuthValue)
	}
	return p, nil
}

// Update changes a server's settings (team editors and above); the slug
// never changes. A new URL or header forgets the stored health. A new URL
// also forgets when capabilities were read and reads them again from the new
// endpoint, so nothing is approved on the strength of the old one. Audited as
// mcp_server.update with whether the header value changed (never the value).
func (s *Service) Update(ctx context.Context, a authz.Actor, ref string, id uuid.UUID, in UpdateInput, expectedRevision int64) (ServerView, error) {
	var moved bool
	err := store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		ta, err := access(ctx, q, a, ref)
		if err != nil {
			return err
		}
		if err := ta.writable(authz.CheckServerEdit); err != nil {
			return err
		}
		cur, err := teamServer(ctx, q, ta.team.ID, id, true)
		if err != nil {
			return err
		}
		if cur.Revision != expectedRevision {
			return apperr.Stale()
		}
		p, valueChanged, err := s.applyUpdate(ctx, cur, in)
		if err != nil {
			return err
		}
		if in.SetRequirement {
			if p.AuthProviderID, p.AuthScopes, err = checkRequirement(ctx, q, in.Requirement, cur.AuthProviderID); err != nil {
				return err
			}
		}
		ps, err := LoadProviders(ctx, q)
		if err != nil {
			return err
		}
		before, err := effectiveAll(ctx, q, cur, ps)
		if err != nil {
			return err
		}
		p.UpdatedBy = by(a)
		row, err := q.UpdateMCPServer(ctx, p)
		if err != nil {
			return err
		}
		reset := int64(0)
		if row.AuthProviderID != cur.AuthProviderID {
			// Overrides with other scopes belong to the old provider.
			if reset, err = q.ResetMCPCapabilityScopeOverrides(ctx, id); err != nil {
				return err
			}
		}
		after, err := effectiveAll(ctx, q, row, ps)
		if err != nil {
			return err
		}
		e := a.Audit("mcp_server.update", "mcp_server", id.String())
		e.TeamID, e.Before, e.After = ta.team.ID, snapshot(cur), snapshot(row)
		e.Metadata = map[string]any{"authValueChanged": valueChanged}
		if reset > 0 {
			e.Metadata["overridesReset"] = reset
		}
		if err := audit.Record(ctx, q, e); err != nil {
			return err
		}
		switch {
		case row.URL != cur.URL:
			err = resubmit(ctx, q, a, ta.team, cur, "url_changed", "The server's URL changed; review it again.")
		case widened(before, after):
			err = resubmit(ctx, q, a, ta.team, cur, "requirement_changed", noteRequirementChanged)
		}
		if err != nil {
			return err
		}
		if moved = row.URL != cur.URL; moved {
			if err := q.ClearMCPServerRefreshed(ctx, id); err != nil {
				return err
			}
		}
		// A new address or credentials make the stored health meaningless.
		if valueChanged || row.URL != cur.URL || !equalPtr(row.AuthHeaderName, cur.AuthHeaderName) {
			return q.ForgetMCPHealthChecks(ctx, id)
		}
		return nil
	})
	if err != nil {
		return ServerView{}, err
	}
	if moved {
		// Best effort, like Create: a failure is stored as the refresh error.
		sv, err := s.q.GetMCPServer(ctx, id)
		if err != nil {
			return ServerView{}, err
		}
		if sv.Enabled {
			if _, _, err := s.refresh(ctx, a, sv); err != nil {
				return ServerView{}, err
			}
		}
	}
	return s.view(ctx, s.q, id)
}

func (s *Service) applyUpdate(ctx context.Context, cur dbgen.McpServer, in UpdateInput) (dbgen.UpdateMCPServerParams, bool, error) {
	p := dbgen.UpdateMCPServerParams{ID: cur.ID, Name: cur.Name, Description: cur.Description, URL: cur.URL,
		AuthHeaderName: cur.AuthHeaderName, AuthValueCipher: cur.AuthValueCipher, AuthValueHint: cur.AuthValueHint,
		TimeoutSeconds: cur.TimeoutSeconds, Enabled: cur.Enabled, AuthProviderID: cur.AuthProviderID, AuthScopes: nonNil(cur.AuthScopes)}
	setIf(&p.Name, in.Name)
	p.Name = strings.TrimSpace(p.Name)
	setIf(&p.Description, in.Description)
	setIf(&p.TimeoutSeconds, in.TimeoutSeconds)
	setIf(&p.Enabled, in.Enabled)
	if err := checkFields(p.Name, p.Description, p.TimeoutSeconds); err != nil {
		return p, false, err
	}
	if in.URL != nil {
		u, err := s.client.NormalizeURL(ctx, *in.URL)
		if err != nil {
			return p, false, err
		}
		p.URL = u
	}
	removed := in.AuthHeaderName != nil && *in.AuthHeaderName == "" && cur.AuthValueCipher != nil
	return p, in.AuthValue != nil || removed, s.applyAuth(&p, cur, in)
}

// applyAuth applies a header change: "" removes it; a new name keeps the
// stored value unless a new one is given.
func (s *Service) applyAuth(p *dbgen.UpdateMCPServerParams, cur dbgen.McpServer, in UpdateInput) error {
	if in.AuthHeaderName != nil && *in.AuthHeaderName == "" {
		if in.AuthValue != nil && *in.AuthValue != "" {
			return apperr.Invalid("invalid_auth_header", "Enter the header's name with its value")
		}
		p.AuthHeaderName, p.AuthValueCipher, p.AuthValueHint = nil, nil, ""
		return nil
	}
	name := ""
	if cur.AuthHeaderName != nil {
		name = *cur.AuthHeaderName
	}
	setIf(&name, in.AuthHeaderName)
	if in.AuthValue == nil {
		if in.AuthHeaderName == nil {
			return nil
		}
		if cur.AuthValueCipher == nil {
			return apperr.Invalid("invalid_auth_value", "Enter the header's value")
		}
		if err := checkHeader(name, "x"); err != nil {
			return err
		}
		p.AuthHeaderName = &name
		return nil
	}
	if err := checkHeader(name, *in.AuthValue); err != nil {
		return err
	}
	ct, err := s.box.Seal([]byte(*in.AuthValue), ServerAAD(cur.ID))
	if err != nil {
		return err
	}
	p.AuthHeaderName, p.AuthValueCipher, p.AuthValueHint = &name, ct, valueHint(*in.AuthValue)
	return nil
}

func setIf[T any](dst *T, v *T) {
	if v != nil {
		*dst = *v
	}
}

func equalPtr(a, b *string) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }

// Delete removes a server with its capabilities and health (team owners and
// admins). Audited as mcp_server.delete.
func (s *Service) Delete(ctx context.Context, a authz.Actor, ref string, id uuid.UUID) error {
	return store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		ta, err := access(ctx, q, a, ref)
		if err != nil {
			return err
		}
		if err := ta.writable(authz.CheckServerManage); err != nil {
			return err
		}
		cur, err := teamServer(ctx, q, ta.team.ID, id, true)
		if err != nil {
			return err
		}
		if err := q.DeleteMCPServer(ctx, id); err != nil {
			return err
		}
		e := a.Audit("mcp_server.delete", "mcp_server", id.String())
		e.TeamID, e.Before = ta.team.ID, snapshot(cur)
		return audit.Record(ctx, q, e)
	})
}

// Target is how to reach a server for a forwarded call (internal/mcpgw):
// see target.
func (s *Service) Target(ctx context.Context, sv dbgen.McpServer) (upstream.Target, error) {
	return s.target(ctx, sv)
}

// Client is the guarded client servers are reached with.
func (s *Service) Client() *upstream.Client { return s.client }

// target reads how to reach a server, decrypting its header value. The URL
// is checked against the address rule again (it may have been saved under
// the development allowance, or the allowlist may have changed since).
func (s *Service) target(ctx context.Context, sv dbgen.McpServer) (upstream.Target, error) {
	t := upstream.Target{URL: sv.URL, Timeout: timeout(sv)}
	if _, err := s.client.NormalizeURL(ctx, sv.URL); err != nil {
		msg := errNotAllowedNow.Message
		if e, ok := apperr.As(err); ok {
			msg = "The server's URL is not allowed: " + e.Message
		}
		return t, &upstream.Error{Class: upstream.ClassNotAllowed, Message: msg}
	}
	if sv.AuthHeaderName != nil && sv.AuthValueCipher != nil {
		plain, err := s.box.Open(sv.AuthValueCipher, ServerAAD(sv.ID))
		if err != nil {
			return t, errUndecryptable
		}
		t.HeaderName, t.HeaderValue = *sv.AuthHeaderName, string(plain)
	}
	return t, nil
}
