package registry

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ncecere/open-mcp-gateway/internal/apperr"
	"github.com/ncecere/open-mcp-gateway/internal/audit"
	"github.com/ncecere/open-mcp-gateway/internal/authz"
	"github.com/ncecere/open-mcp-gateway/internal/store"
	"github.com/ncecere/open-mcp-gateway/internal/store/dbgen"
)

// User-authorization requirements (docs/phase5-delegated-credentials.md,
// decision 26): a server may require an auth provider, whose user token the
// gateway sends with calls to its capabilities, with scopes (none: the
// provider's default scopes). A capability inherits it, opts out (none), or
// asks for other scopes of the same provider (scopes). The token always
// goes to the server: overrides decide where consent is asked and which
// scopes are sent, not which of the server's code sees a token.

// Requirement modes of a capability (mcp_capabilities.requirement_mode).
const (
	ModeInherit = "inherit"
	ModeNone    = "none"
	ModeScopes  = "scopes"
)

// MaxScopes bounds a scope list.
const MaxScopes = 50

// scopeRE is an RFC 6749 scope token: printable ASCII but space, " and \.
var scopeRE = regexp.MustCompile(`^[!#-\[\]-~]{1,200}$`)

// NormalizeScopes checks a scope list (400 invalid_scope with
// details.scope) and drops duplicates, keeping the order. The result is
// never nil.
func NormalizeScopes(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !scopeRE.MatchString(s) {
			e := apperr.Invalid("invalid_scope", "Scopes are 1-200 printable characters without spaces, quotes or backslashes")
			e.Details = map[string]any{"scope": s}
			return nil, e
		}
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	if len(out) > MaxScopes {
		e := apperr.Invalid("invalid_scope", "At most 50 scopes")
		e.Details = map[string]any{"max": MaxScopes}
		return nil, e
	}
	return out, nil
}

// Provider is what requirements need to know about an auth provider.
type Provider struct {
	ID            uuid.UUID
	Slug, Name    string
	Enabled       bool
	DefaultScopes []string
	Preset        *string
	LogoURL       *string
}

// Providers are the auth providers by ID.
type Providers map[uuid.UUID]Provider

// LoadProviders reads every auth provider (there are at most 100).
func LoadProviders(ctx context.Context, q *dbgen.Queries) (Providers, error) {
	rows, err := q.ListAllAuthProviders(ctx)
	if err != nil {
		return nil, err
	}
	out := make(Providers, len(rows))
	for _, p := range rows {
		out[p.ID] = ProviderOf(p)
	}
	return out, nil
}

// ProviderOf is a provider row as requirements see it (never its secret).
func ProviderOf(p dbgen.AuthProvider) Provider {
	scopes := p.DefaultScopes
	if scopes == nil {
		scopes = []string{}
	}
	return Provider{ID: p.ID, Slug: p.Slug, Name: p.Name, Enabled: p.Enabled, DefaultScopes: scopes, Preset: p.Preset, LogoURL: p.LogoUrl}
}

// ServerRequirement is a server's requirement as stored: Scopes empty
// means the provider's default scopes.
type ServerRequirement struct {
	Provider Provider
	Scopes   []string
}

// Requirement is what a call of a capability needs: the provider, and the
// effective scopes (the override's, the server's, or the provider's
// default scopes).
type Requirement struct {
	Provider Provider
	Scopes   []string
}

// Same reports whether two effective requirements send the same thing.
func (r *Requirement) Same(o *Requirement) bool {
	if r == nil || o == nil {
		return r == nil && o == nil
	}
	return r.Provider.ID == o.Provider.ID && slices.Equal(sorted(r.Scopes), sorted(o.Scopes))
}

func sorted(s []string) []string {
	out := slices.Clone(s)
	slices.Sort(out)
	return out
}

// Widens reports whether after sends more than before: a provider where
// there was none, another provider, or a scope that wasn't in effect.
func Widens(before, after *Requirement) bool {
	switch {
	case after == nil:
		return false
	case before == nil || before.Provider.ID != after.Provider.ID:
		return true
	}
	for _, s := range after.Scopes {
		if !slices.Contains(before.Scopes, s) {
			return true
		}
	}
	return false
}

// RequirementOf is a server's requirement (nil: none). A provider that no
// longer exists can't be required (the foreign key restricts deleting it).
func RequirementOf(sv dbgen.McpServer, ps Providers) *ServerRequirement {
	if !sv.AuthProviderID.Valid {
		return nil
	}
	p, ok := ps[sv.AuthProviderID.UUID]
	if !ok {
		return nil
	}
	return &ServerRequirement{Provider: p, Scopes: nonNil(sv.AuthScopes)}
}

// Effective is the requirement of a capability of a server: none when the
// capability opts out or the server has none; otherwise the server's
// provider with the override's scopes, the server's, or the provider's
// default scopes.
func Effective(serverProvider uuid.NullUUID, serverScopes []string, mode string, capScopes []string, ps Providers) *Requirement {
	if mode == ModeNone || !serverProvider.Valid {
		return nil
	}
	p, ok := ps[serverProvider.UUID]
	if !ok {
		return nil
	}
	scopes := serverScopes
	switch {
	case mode == ModeScopes && len(capScopes) > 0:
		scopes = capScopes
	case len(scopes) == 0:
		scopes = p.DefaultScopes
	}
	return &Requirement{Provider: p, Scopes: nonNil(slices.Clone(scopes))}
}

// EffectiveFor is Effective for a capability row of a server row.
func EffectiveFor(sv dbgen.McpServer, c dbgen.McpCapability, ps Providers) *Requirement {
	return Effective(sv.AuthProviderID, sv.AuthScopes, c.RequirementMode, c.RequirementScopes, ps)
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// RequirementInput is a server's new requirement: a provider and scopes
// (empty: the provider's default scopes).
type RequirementInput struct {
	ProviderID uuid.UUID
	Scopes     []string
}

var errProviderNotFound = apperr.New(http.StatusUnprocessableEntity, "provider_not_found",
	"Choose an auth provider that exists and is turned on")

// checkRequirement validates a requirement to store. A newly chosen
// provider must exist and be enabled; keep is the provider the server has
// now, which may stay even while turned off.
func checkRequirement(ctx context.Context, q *dbgen.Queries, in *RequirementInput, keep uuid.NullUUID) (uuid.NullUUID, []string, error) {
	if in == nil {
		return uuid.NullUUID{}, []string{}, nil
	}
	scopes, err := NormalizeScopes(in.Scopes)
	if err != nil {
		return uuid.NullUUID{}, nil, err
	}
	p, err := q.GetAuthProvider(ctx, in.ProviderID)
	if errors.Is(store.NotFound(err), store.ErrNotFound) {
		return uuid.NullUUID{}, nil, errProviderNotFound
	} else if err != nil {
		return uuid.NullUUID{}, nil, err
	}
	if !p.Enabled && !(keep.Valid && keep.UUID == p.ID) {
		return uuid.NullUUID{}, nil, errProviderNotFound
	}
	return uuid.NullUUID{UUID: p.ID, Valid: true}, scopes, nil
}

// requirementSnapshot is a server's requirement as audited.
func requirementSnapshot(sv dbgen.McpServer) any {
	if !sv.AuthProviderID.Valid {
		return nil
	}
	return map[string]any{"providerId": sv.AuthProviderID.UUID.String(), "scopes": nonNil(sv.AuthScopes)}
}

// effectiveAll is every capability's effective requirement, by ID.
func effectiveAll(ctx context.Context, q *dbgen.Queries, sv dbgen.McpServer, ps Providers) (map[uuid.UUID]*Requirement, error) {
	rows, err := q.ListMCPCapabilities(ctx, dbgen.ListMCPCapabilitiesParams{ServerID: sv.ID})
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]*Requirement, len(rows))
	for _, r := range rows {
		out[r.McpCapability.ID] = EffectiveFor(sv, r.McpCapability, ps)
	}
	return out, nil
}

// widened reports whether any capability's effective requirement widens
// between before and after (same capability IDs; ones only in after count
// as widening from none).
func widened(before, after map[uuid.UUID]*Requirement) bool {
	for id, a := range after {
		if Widens(before[id], a) {
			return true
		}
	}
	return false
}

// resubmit sends a published server back for review under
// approval_required (reason url_changed or requirement_changed). Under
// self_publish the team approves its own changes, so the server stays
// published. Audited as mcp_server.publish_request.
func resubmit(ctx context.Context, q *dbgen.Queries, a authz.Actor, team dbgen.Team, sv dbgen.McpServer, reason, note string) error {
	if sv.PublishState != StatePublished {
		return nil
	}
	def, err := platformDefault(ctx, q)
	if err != nil {
		return err
	}
	if authz.EffectivePolicy(team.PublishPolicyOverride, def) != authz.PolicyApprovalRequired {
		return nil
	}
	if _, err := q.SetMCPServerPublishState(ctx, dbgen.SetMCPServerPublishStateParams{
		ID: sv.ID, PublishState: StatePending, Requested: true, Actor: by(a), Note: &note,
	}); err != nil {
		return err
	}
	e := a.Audit("mcp_server.publish_request", "mcp_server", sv.ID.String())
	e.TeamID = team.ID
	e.Before, e.After = map[string]any{"publishState": StatePublished}, map[string]any{"publishState": StatePending}
	e.Metadata = map[string]any{"name": sv.Name, "reason": reason, "note": note}
	return audit.Record(ctx, q, e)
}

const noteRequirementChanged = "The server's user authorization requirement now sends more; review it again."

// OverrideInput is a capability's requirement override.
type OverrideInput struct {
	Mode   string
	Scopes *[]string
}

// SetCapabilityRequirement overrides a capability's requirement (team
// editors and above). expectedRevision is the server's, which this bumps.
// Under approval_required a published server goes back to review when the
// override widens what the capability receives. Audited as
// mcp_capability.requirement_set.
func (s *Service) SetCapabilityRequirement(ctx context.Context, a authz.Actor, ref string, serverID, capID uuid.UUID, in OverrideInput, expectedRevision int64) (CapabilityView, int64, error) {
	var scopes []string
	switch in.Mode {
	case ModeInherit, ModeNone:
		if in.Scopes != nil {
			return CapabilityView{}, 0, apperr.Invalid("invalid_mode", "Send scopes only with mode scopes")
		}
		scopes = []string{}
	case ModeScopes:
		if in.Scopes == nil || len(*in.Scopes) == 0 {
			return CapabilityView{}, 0, apperr.Invalid("invalid_mode", "Mode scopes needs at least one scope")
		}
		var err error
		if scopes, err = NormalizeScopes(*in.Scopes); err != nil {
			return CapabilityView{}, 0, err
		}
	default:
		return CapabilityView{}, 0, apperr.Invalid("invalid_mode", "mode must be inherit, none or scopes")
	}
	var out CapabilityView
	var revision int64
	err := store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		ta, err := access(ctx, q, a, ref)
		if err != nil {
			return err
		}
		if err := ta.writable(authz.CheckServerEdit); err != nil {
			return err
		}
		sv, err := teamServer(ctx, q, ta.team.ID, serverID, true)
		if err != nil {
			return err
		}
		if sv.Revision != expectedRevision {
			return apperr.Stale()
		}
		cur, err := q.LockMCPCapability(ctx, dbgen.LockMCPCapabilityParams{ID: capID, ServerID: sv.ID})
		if errors.Is(store.NotFound(err), store.ErrNotFound) {
			return errNoCapability
		} else if err != nil {
			return err
		}
		if in.Mode == ModeScopes && !sv.AuthProviderID.Valid {
			return apperr.Conflict("no_server_requirement", "Set the server's user authorization requirement first")
		}
		ps, err := LoadProviders(ctx, q)
		if err != nil {
			return err
		}
		before := EffectiveFor(sv, cur, ps)
		row, err := q.SetMCPCapabilityRequirement(ctx, dbgen.SetMCPCapabilityRequirementParams{
			ID: capID, ServerID: sv.ID, RequirementMode: in.Mode, RequirementScopes: scopes,
		})
		if err != nil {
			return err
		}
		bumped, err := q.BumpMCPServerRevision(ctx, dbgen.BumpMCPServerRevisionParams{ID: sv.ID, UpdatedBy: by(a)})
		if err != nil {
			return err
		}
		revision = bumped.Revision
		after := EffectiveFor(bumped, row, ps)
		e := a.Audit("mcp_capability.requirement_set", "mcp_capability", capID.String())
		e.TeamID = ta.team.ID
		e.Before = map[string]any{"mode": cur.RequirementMode, "scopes": nonNil(cur.RequirementScopes)}
		e.After = map[string]any{"mode": row.RequirementMode, "scopes": nonNil(row.RequirementScopes)}
		e.Metadata = map[string]any{"serverId": sv.ID, "server": sv.Name, "kind": cur.Kind, "name": sv.Name + " · " + cur.Name,
			"capability": cur.Name, "widened": Widens(before, after)}
		if err := audit.Record(ctx, q, e); err != nil {
			return err
		}
		if Widens(before, after) {
			if err := resubmit(ctx, q, a, ta.team, bumped, "requirement_changed", noteRequirementChanged); err != nil {
				return err
			}
			if bumped, err = q.GetMCPServer(ctx, sv.ID); err != nil {
				return err
			}
			revision = bumped.Revision
		}
		name := ""
		if r, err := q.ListMCPCapabilities(ctx, dbgen.ListMCPCapabilitiesParams{ServerID: sv.ID}); err == nil {
			for _, c := range r {
				if c.McpCapability.ID == capID {
					name = c.ApprovedByName
				}
			}
		}
		out = CapabilityView{Capability: row, ApprovedByName: name, Published: bumped.PublishState == StatePublished, Requires: after}
		return nil
	})
	return out, revision, err
}
