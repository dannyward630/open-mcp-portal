package gateways

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ncecere/open-mcp-gateway/internal/apperr"
	"github.com/ncecere/open-mcp-gateway/internal/audit"
	"github.com/ncecere/open-mcp-gateway/internal/authz"
	"github.com/ncecere/open-mcp-gateway/internal/registry"
	"github.com/ncecere/open-mcp-gateway/internal/store"
	"github.com/ncecere/open-mcp-gateway/internal/store/dbgen"
	"github.com/ncecere/open-mcp-gateway/internal/upstream"
)

// Selections: which tools and prompts a gateway exposes, and whether each
// is served right now.

// Selection limits.
const (
	MaxSelection = 200
	MaxOptions   = 1000
)

// Why a selected capability isn't served (the API's GatewayUnavailableReason).
const (
	ReasonServerDisabled = "server_disabled"
	ReasonNotPublished   = "not_published"
	ReasonNotApproved    = "not_approved"
	ReasonChanged        = "changed"
	ReasonGone           = "gone"
)

// Option sources.
const (
	SourceTeam    = "team"
	SourceCatalog = "catalog"
)

// Selectable reports whether a capability kind may be selected (Phase 3:
// tools and prompts; resources come later).
func Selectable(kind string) bool { return kind == upstream.KindTool || kind == upstream.KindPrompt }

// ExposedName is the name clients see: {serverSlug}__{name}. Server slugs
// have no underscores, so the first "__" always ends the slug.
func ExposedName(serverSlug, name string) string { return serverSlug + "__" + name }

// Availability says whether a gateway serves a selected capability now,
// and why not: it must be listed, of a selectable kind, its server enabled,
// and either a server of the team gateway's own team, or published with the
// current definition approved. GatewayStats (SQL) mirrors this rule.
func Availability(g dbgen.Gateway, c dbgen.McpCapability, s dbgen.McpServer) (bool, string) {
	switch {
	case c.GoneAt != nil || !Selectable(c.Kind):
		return false, ReasonGone
	case !s.Enabled:
		return false, ReasonServerDisabled
	case g.OwnerKind == OwnerTeam && g.TeamID.Valid && s.TeamID == g.TeamID.UUID:
		return true, ""
	case s.PublishState != registry.StatePublished:
		return false, ReasonNotPublished
	case c.ApprovedFingerprint == nil:
		return false, ReasonNotApproved
	case *c.ApprovedFingerprint != c.Fingerprint:
		return false, ReasonChanged
	}
	return true, ""
}

// Selected is one selected capability.
type Selected struct {
	Capability         dbgen.McpCapability
	Server             dbgen.McpServer
	TeamSlug, TeamName string // the server's team
	MinRole            *string
	ExposedName        string
	Available          bool
	Reason             string // when not Available
}

// Selection is a gateway's selection with its revision.
type Selection struct {
	Revision int64
	Items    []Selected
}

func selection(ctx context.Context, q *dbgen.Queries, g dbgen.Gateway) ([]Selected, error) {
	rows, err := q.ListGatewaySelection(ctx, g.ID)
	if err != nil {
		return nil, err
	}
	out := make([]Selected, len(rows))
	for i, r := range rows {
		ok, reason := Availability(g, r.McpCapability, r.McpServer)
		out[i] = Selected{Capability: r.McpCapability, Server: r.McpServer, TeamSlug: r.TeamSlug, TeamName: r.TeamName,
			MinRole: r.MinTeamRole, ExposedName: ExposedName(r.McpServer.Slug, r.McpCapability.Name), Available: ok, Reason: reason}
	}
	return out, nil
}

// Option is a capability a gateway may select.
type Option struct {
	dbgen.ListGatewayOptionsRow
	Source      string
	ExposedName string
}

func checkKind(kind *string) error {
	if kind != nil && !Selectable(*kind) {
		return apperr.Invalid("invalid_kind", "kind must be tool or prompt")
	}
	return nil
}

// options lists what a gateway of teamID (uuid.Nil: a platform gateway) may
// select; ids narrows to those capabilities.
func options(ctx context.Context, q *dbgen.Queries, teamID uuid.UUID, search, kind *string, ids []uuid.UUID) ([]Option, error) {
	p := dbgen.ListGatewayOptionsParams{Kind: kind, PageSize: MaxOptions, Ids: []uuid.UUID{}}
	if teamID != uuid.Nil {
		p.TeamID = uuid.NullUUID{UUID: teamID, Valid: true}
	}
	if search != nil {
		escaped := store.EscapeLike(*search)
		p.Search = &escaped
	}
	if ids != nil {
		p.ByIds, p.Ids, p.PageSize = true, ids, int32(len(ids))
	}
	rows, err := q.ListGatewayOptions(ctx, p)
	if err != nil {
		return nil, err
	}
	out := make([]Option, len(rows))
	for i, r := range rows {
		src := SourceCatalog
		if teamID != uuid.Nil && r.ServerTeamID == teamID {
			src = SourceTeam
		}
		out[i] = Option{ListGatewayOptionsRow: r, Source: src, ExposedName: ExposedName(r.ServerSlug, r.Name)}
	}
	return out, nil
}

// TeamSelection returns a team gateway's selection (members; platform
// admins and auditors).
func (s *Service) TeamSelection(ctx context.Context, a authz.Actor, ref string, id uuid.UUID) (Selection, error) {
	ta, err := access(ctx, s.q, a, ref)
	if err != nil {
		return Selection{}, err
	}
	g, err := teamGateway(ctx, s.q, ta.team.ID, id, false)
	if err != nil {
		return Selection{}, err
	}
	items, err := selection(ctx, s.q, g)
	return Selection{Revision: g.Revision, Items: items}, err
}

// AdminSelection returns any gateway's selection (platform admins and
// auditors).
func (s *Service) AdminSelection(ctx context.Context, a authz.Actor, id uuid.UUID) (Selection, error) {
	if !a.CanReadPlatform() {
		return Selection{}, errPlatformRead
	}
	g, err := getGateway(ctx, s.q, id, false)
	if err != nil {
		return Selection{}, err
	}
	items, err := selection(ctx, s.q, g)
	return Selection{Revision: g.Revision, Items: items}, err
}

// TeamOptions lists what the team's gateways may select (team owners and
// admins).
func (s *Service) TeamOptions(ctx context.Context, a authz.Actor, ref string, search, kind *string) ([]Option, error) {
	ta, err := access(ctx, s.q, a, ref)
	if err != nil {
		return nil, err
	}
	if err := authz.CheckGatewayEdit(ta.role); err != nil {
		return nil, err
	}
	if err := checkKind(kind); err != nil {
		return nil, err
	}
	return options(ctx, s.q, ta.team.ID, search, kind, nil)
}

// AdminOptions lists what platform gateways may select (platform admins
// and auditors).
func (s *Service) AdminOptions(ctx context.Context, a authz.Actor, search, kind *string) ([]Option, error) {
	if !a.CanReadPlatform() {
		return nil, errPlatformRead
	}
	if err := checkKind(kind); err != nil {
		return nil, err
	}
	return options(ctx, s.q, uuid.Nil, search, kind, nil)
}

// SelectionInput is one capability of a new selection.
type SelectionInput struct {
	CapabilityID uuid.UUID
	MinRole      *string
}

// SetTeamSelection replaces a team gateway's selection (team owners and
// admins); expectedRevision is the gateway's.
func (s *Service) SetTeamSelection(ctx context.Context, a authz.Actor, ref string, id uuid.UUID, items []SelectionInput, expectedRevision int64) (Selection, error) {
	var out Selection
	err := store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		ta, err := access(ctx, q, a, ref)
		if err != nil {
			return err
		}
		if err := ta.writable(); err != nil {
			return err
		}
		g, err := teamGateway(ctx, q, ta.team.ID, id, true)
		if err != nil {
			return err
		}
		out, err = setSelection(ctx, q, a, g, items, expectedRevision)
		return err
	})
	return out, err
}

// AdminSetSelection replaces a platform gateway's selection (platform
// admins; 403 team_gateway for a team gateway).
func (s *Service) AdminSetSelection(ctx context.Context, a authz.Actor, id uuid.UUID, items []SelectionInput, expectedRevision int64) (Selection, error) {
	if !a.IsPlatformAdmin() {
		return Selection{}, errAdminOnly
	}
	var out Selection
	err := store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		g, err := getGateway(ctx, q, id, true)
		if err != nil {
			return err
		}
		if g.OwnerKind != OwnerPlatform {
			return errTeamGateway
		}
		out, err = setSelection(ctx, q, a, g, items, expectedRevision)
		return err
	})
	return out, err
}

func unprocessable(code, msg string, details any) error {
	e := apperr.New(http.StatusUnprocessableEntity, code, msg)
	e.Details = details
	return e
}

// audited is a selection as the audit log records it.
func audited(items []Selected) []map[string]any {
	out := make([]map[string]any, len(items))
	for i, it := range items {
		m := map[string]any{"exposedName": it.ExposedName, "kind": it.Capability.Kind}
		if it.MinRole != nil {
			m["minRole"] = *it.MinRole
		}
		out[i] = m
	}
	return out
}

// setSelection validates and stores a gateway's new selection (g is
// locked). New capabilities must be offered by the gateway's options;
// capabilities already selected may stay even when they aren't served right
// now (a catalog server that left the catalog), since every call re-checks
// them. minRole is for team gateways only, and exposed names must be
// distinct (case-insensitively, across tools and prompts). Audited as
// gateway.capabilities_set.
func setSelection(ctx context.Context, q *dbgen.Queries, a authz.Actor, g dbgen.Gateway, items []SelectionInput, expectedRevision int64) (Selection, error) {
	if g.Revision != expectedRevision {
		return Selection{}, apperr.Stale()
	}
	if len(items) > MaxSelection {
		return Selection{}, unprocessable("too_many", "A gateway can select at most 200 capabilities", map[string]any{"max": MaxSelection})
	}
	before, err := selection(ctx, q, g)
	if err != nil {
		return Selection{}, err
	}
	current := make(map[uuid.UUID]Selected, len(before))
	for _, it := range before {
		current[it.Capability.ID] = it
	}
	seen := map[uuid.UUID]bool{}
	var added []uuid.UUID
	for _, it := range items {
		if seen[it.CapabilityID] {
			return Selection{}, apperr.Invalid("duplicate_capability", "Each capability can be selected once")
		}
		seen[it.CapabilityID] = true
		if it.MinRole != nil {
			if g.OwnerKind != OwnerTeam {
				return Selection{}, unprocessable("min_role_not_allowed", "Platform gateways have no minimum team role",
					map[string]any{"capabilityIds": []uuid.UUID{it.CapabilityID}})
			}
			if !authz.ValidTeamRole(*it.MinRole) {
				return Selection{}, apperr.Invalid("invalid_role", "minRole must be owner, admin, editor or member")
			}
		}
		if _, ok := current[it.CapabilityID]; !ok {
			added = append(added, it.CapabilityID)
		}
	}
	names := map[uuid.UUID]string{}
	for id, it := range current {
		names[id] = it.ExposedName
	}
	if len(added) > 0 {
		offered, err := options(ctx, q, teamOf(g), nil, nil, added)
		if err != nil {
			return Selection{}, err
		}
		ok := map[uuid.UUID]bool{}
		for _, o := range offered {
			ok[o.ID], names[o.ID] = true, o.ExposedName
		}
		refused := []uuid.UUID{}
		for _, id := range added {
			if !ok[id] {
				refused = append(refused, id)
			}
		}
		if len(refused) > 0 {
			return Selection{}, unprocessable("capability_not_selectable",
				"Some capabilities can't be selected for this gateway: another team's private server, not approved, gone, or not a tool or prompt",
				map[string]any{"capabilityIds": refused})
		}
	}
	exposed := map[string]bool{}
	keep := make([]uuid.UUID, 0, len(items))
	for _, it := range items {
		n := strings.ToLower(names[it.CapabilityID])
		if exposed[n] {
			return Selection{}, unprocessable("name_clash", "Two selected capabilities expose the same name",
				map[string]any{"exposedName": names[it.CapabilityID]})
		}
		exposed[n] = true
		keep = append(keep, it.CapabilityID)
	}
	if err := q.RemoveGatewayCapabilitiesExcept(ctx, dbgen.RemoveGatewayCapabilitiesExceptParams{GatewayID: g.ID, Keep: keep}); err != nil {
		return Selection{}, err
	}
	for _, it := range items {
		if err := q.UpsertGatewayCapability(ctx, dbgen.UpsertGatewayCapabilityParams{
			GatewayID: g.ID, CapabilityID: it.CapabilityID, MinTeamRole: it.MinRole, AddedBy: by(a),
		}); err != nil {
			return Selection{}, err
		}
	}
	row, err := q.BumpGatewayRevision(ctx, dbgen.BumpGatewayRevisionParams{ID: g.ID, UpdatedBy: by(a)})
	if err != nil {
		return Selection{}, err
	}
	after, err := selection(ctx, q, row)
	if err != nil {
		return Selection{}, err
	}
	e := a.Audit("gateway.capabilities_set", "gateway", g.ID.String())
	e.TeamID, e.Before, e.After = teamOf(g), map[string]any{"capabilities": audited(before)}, map[string]any{"capabilities": audited(after)}
	e.Metadata = map[string]any{"name": g.Name, "added": len(added), "removed": len(before) + len(added) - len(items), "count": len(items)}
	if err := audit.Record(ctx, q, e); err != nil {
		return Selection{}, err
	}
	return Selection{Revision: row.Revision, Items: after}, nil
}
