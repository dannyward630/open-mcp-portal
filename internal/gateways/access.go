package gateways

import (
	"context"
	"errors"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ncecere/open-mcp-gateway/internal/apperr"
	"github.com/ncecere/open-mcp-gateway/internal/audit"
	"github.com/ncecere/open-mcp-gateway/internal/authz"
	"github.com/ncecere/open-mcp-gateway/internal/store"
	"github.com/ncecere/open-mcp-gateway/internal/store/dbgen"
	"github.com/ncecere/open-mcp-gateway/internal/teams"
)

// Who may use a gateway: a team gateway's members, and the teams, people
// and SSO groups a platform gateway is granted to.

// Grant subject kinds.
const (
	SubjectTeam  = "team"
	SubjectUser  = "user"
	SubjectGroup = "group"
)

// Why a person may use a gateway (the API's MyGatewayAccess).
const (
	AccessTeamMember = "team_member"
	AccessTeamGrant  = "team_grant"
	AccessUserGrant  = "user_grant"
	AccessGroupGrant = "group_grant"
)

// DeniedRole is why a selected, served capability is refused to a member
// whose role is below its minimum.
const DeniedRole = "role"

var (
	errNoGrant         = apperr.NotFound("grant_not_found", "Grant not found")
	errGrantExists     = apperr.Conflict("grant_exists", "The gateway is already granted to them")
	errSubjectNotFound = apperr.NotFound("subject_not_found", "No such team or user")
	errInvalidGrant    = apperr.Invalid("invalid_grant", "Give exactly one of team, userEmail or group, matching subjectKind")
)

var grantAccess = map[string]string{SubjectTeam: AccessTeamGrant, SubjectUser: AccessUserGrant, SubjectGroup: AccessGroupGrant}

// accessOrder sorts access reasons.
var accessOrder = []string{AccessTeamMember, AccessTeamGrant, AccessUserGrant, AccessGroupGrant}

func grantKinds(kinds []string) []string {
	out := []string{}
	for _, a := range accessOrder {
		for _, k := range kinds {
			if grantAccess[k] == a {
				out = append(out, a)
			}
		}
	}
	return out
}

func groupsOf(u dbgen.User) []string {
	if u.Groups == nil {
		return []string{}
	}
	return u.Groups
}

// Caller is a person's access to one gateway.
type Caller struct {
	// Role is the person's role in a team gateway's team.
	Role string
	// Access says why they may use it (empty: they may not).
	Access []string
}

// Allowed reports whether the person may use the gateway.
func (c Caller) Allowed() bool { return len(c.Access) > 0 }

// CallerOf works out a person's access to a gateway from their current
// memberships, grants and stored groups. It doesn't look at whether the
// gateway or the person is enabled.
func (s *Service) CallerOf(ctx context.Context, g dbgen.Gateway, u dbgen.User) (Caller, error) {
	return callerOf(ctx, s.q, g, u)
}

func callerOf(ctx context.Context, q *dbgen.Queries, g dbgen.Gateway, u dbgen.User) (Caller, error) {
	if g.OwnerKind == OwnerTeam {
		role, err := teams.MemberRole(ctx, q, g.TeamID.UUID, u.ID)
		if err != nil || role == "" {
			return Caller{}, err
		}
		return Caller{Role: role, Access: []string{AccessTeamMember}}, nil
	}
	kinds, err := q.GatewayGrantKindsForUser(ctx, dbgen.GatewayGrantKindsForUserParams{
		GatewayID: g.ID, UserID: uuid.NullUUID{UUID: u.ID, Valid: true}, Groups: groupsOf(u),
	})
	if err != nil {
		return Caller{}, err
	}
	return Caller{Access: grantKinds(kinds)}, nil
}

// Offer is a selected capability as one caller sees it.
type Offer struct {
	Selected
	// Allowed: served now and the caller's role is enough. Otherwise Denied
	// says why (an unavailable reason, or DeniedRole).
	Allowed bool
	Denied  string
}

// Evaluate applies availability and minimum roles to a selection for a
// caller (minRole only applies to team gateways).
func Evaluate(g dbgen.Gateway, items []Selected, c Caller) []Offer {
	out := make([]Offer, len(items))
	for i, it := range items {
		o := Offer{Selected: it}
		switch {
		case !it.Available:
			o.Denied = it.Reason
		case g.OwnerKind == OwnerTeam && !authz.MinRoleAllows(c.Role, it.MinRole):
			o.Denied = DeniedRole
		default:
			o.Allowed = true
		}
		out[i] = o
	}
	return out
}

// Offers is the gateway's selection as the caller sees it, read now.
func (s *Service) Offers(ctx context.Context, g dbgen.Gateway, c Caller) ([]Offer, error) {
	items, err := selection(ctx, s.q, g)
	if err != nil {
		return nil, err
	}
	return Evaluate(g, items, c), nil
}

// Resolve finds a gateway by slug (404 gateway_not_found).
func (s *Service) Resolve(ctx context.Context, slug string) (dbgen.Gateway, error) {
	g, err := s.q.GetGatewayBySlug(ctx, slug)
	if errors.Is(store.NotFound(err), store.ErrNotFound) {
		return g, errNoGateway
	}
	return g, err
}

// Mine is a gateway a person may use.
type Mine struct {
	View   View
	Access []string
	// Offered counts the capabilities it offers them now.
	Offered int64
}

// ListMine lists the enabled gateways a person may use, by name.
func (s *Service) ListMine(ctx context.Context, u dbgen.User) ([]Mine, error) {
	rows, err := s.q.ListGatewaysForUser(ctx, dbgen.ListGatewaysForUserParams{UserID: uuid.NullUUID{UUID: u.ID, Valid: true}, Groups: groupsOf(u)})
	if err != nil {
		return nil, err
	}
	out := make([]Mine, 0, len(rows))
	for _, r := range rows {
		c := Caller{Role: r.MemberRole, Access: grantKinds(r.GrantKinds)}
		if r.Gateway.OwnerKind == OwnerTeam {
			c.Access = []string{AccessTeamMember}
		}
		offers, err := s.Offers(ctx, r.Gateway, c)
		if err != nil {
			return nil, err
		}
		m := Mine{View: View{Gateway: r.Gateway, TeamSlug: r.TeamSlug, TeamName: r.TeamName}, Access: c.Access}
		for _, o := range offers {
			if o.Allowed {
				m.Offered++
			}
		}
		out = append(out, m)
	}
	return out, nil
}

// GetMine returns a gateway the person may use now, with the capabilities
// it offers them; any other is 404.
func (s *Service) GetMine(ctx context.Context, u dbgen.User, id uuid.UUID) (Mine, []Offer, error) {
	g, err := getGateway(ctx, s.q, id, false)
	if err != nil {
		return Mine{}, nil, err
	}
	if !g.Enabled {
		return Mine{}, nil, errNoGateway
	}
	c, err := callerOf(ctx, s.q, g, u)
	if err != nil {
		return Mine{}, nil, err
	}
	if !c.Allowed() {
		return Mine{}, nil, errNoGateway
	}
	v, err := view(ctx, s.q, id)
	if err != nil {
		return Mine{}, nil, err
	}
	offers, err := s.Offers(ctx, g, c)
	if err != nil {
		return Mine{}, nil, err
	}
	allowed := make([]Offer, 0, len(offers))
	for _, o := range offers {
		if o.Allowed {
			allowed = append(allowed, o)
		}
	}
	return Mine{View: v, Access: c.Access, Offered: int64(len(allowed))}, allowed, nil
}

// Grants lists a platform gateway's grants (platform admins and auditors);
// a team gateway has none.
func (s *Service) Grants(ctx context.Context, a authz.Actor, id uuid.UUID) ([]dbgen.ListGatewayGrantsRow, error) {
	if !a.CanReadPlatform() {
		return nil, errPlatformRead
	}
	if _, err := getGateway(ctx, s.q, id, false); err != nil {
		return nil, err
	}
	return s.q.ListGatewayGrants(ctx, id)
}

// GrantInput names a grant's subject: exactly one of Team (slug or ID),
// UserEmail or Group, matching Kind.
type GrantInput struct {
	Kind                   string
	Team, UserEmail, Group *string
}

func validGroup(g string) bool {
	return g != "" && len(g) <= 256 && utf8.ValidString(g) && strings.IndexFunc(g, unicode.IsControl) < 0
}

// AddGrant grants a platform gateway to a team, a person or an SSO group
// (platform admins). Audited as gateway.grant_add.
func (s *Service) AddGrant(ctx context.Context, a authz.Actor, id uuid.UUID, in GrantInput) (dbgen.ListGatewayGrantsRow, error) {
	if !a.IsPlatformAdmin() {
		return dbgen.ListGatewayGrantsRow{}, errAdminOnly
	}
	set := 0
	for _, p := range []*string{in.Team, in.UserEmail, in.Group} {
		if p != nil {
			set++
		}
	}
	if set != 1 {
		return dbgen.ListGatewayGrantsRow{}, errInvalidGrant
	}
	var grantID uuid.UUID
	err := store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		g, err := getGateway(ctx, q, id, true)
		if err != nil {
			return err
		}
		if g.OwnerKind != OwnerPlatform {
			return errTeamGateway
		}
		p := dbgen.InsertGatewayGrantParams{GatewayID: g.ID, SubjectKind: in.Kind, AddedBy: by(a)}
		subject := ""
		switch {
		case in.Kind == SubjectTeam && in.Team != nil:
			t, err := teams.Resolve(ctx, q, strings.TrimSpace(*in.Team))
			if e, ok := apperr.As(err); ok && e.Status == 404 {
				return errSubjectNotFound
			} else if err != nil {
				return err
			}
			p.TeamID, subject = uuid.NullUUID{UUID: t.ID, Valid: true}, t.Slug
		case in.Kind == SubjectUser && in.UserEmail != nil:
			email, err := teams.NormalizeEmail(*in.UserEmail)
			if err != nil {
				return err
			}
			users, err := q.ListUsersByEmail(ctx, email)
			if err != nil {
				return err
			}
			if len(users) == 0 {
				return errSubjectNotFound
			}
			p.UserID, subject = uuid.NullUUID{UUID: users[0].ID, Valid: true}, email
		case in.Kind == SubjectGroup && in.Group != nil:
			group := strings.TrimSpace(*in.Group)
			if !validGroup(group) {
				return apperr.Invalid("invalid_group", "The group must be 1-256 characters on one line")
			}
			p.GroupName, subject = &group, group
		default:
			return errInvalidGrant
		}
		row, err := q.InsertGatewayGrant(ctx, p)
		if apperr.IsUniqueViolation(err, "") {
			return errGrantExists
		} else if err != nil {
			return err
		}
		grantID = row.ID
		e := a.Audit("gateway.grant_add", "gateway_grant", row.ID.String())
		e.After = map[string]any{"subjectKind": in.Kind, "subject": subject}
		e.Metadata = map[string]any{"name": g.Name + " · " + subject, "gatewayId": g.ID.String(), "gatewayName": g.Name}
		return audit.Record(ctx, q, e)
	})
	if err != nil {
		return dbgen.ListGatewayGrantsRow{}, err
	}
	rows, err := s.q.ListGatewayGrants(ctx, id)
	if err != nil {
		return dbgen.ListGatewayGrantsRow{}, err
	}
	i := slices.IndexFunc(rows, func(r dbgen.ListGatewayGrantsRow) bool { return r.GatewayGrant.ID == grantID })
	if i < 0 {
		return dbgen.ListGatewayGrantsRow{}, errNoGrant
	}
	return rows[i], nil
}

// RemoveGrant removes a grant (platform admins); the next call is refused.
// Audited as gateway.grant_remove.
func (s *Service) RemoveGrant(ctx context.Context, a authz.Actor, id, grantID uuid.UUID) error {
	if !a.IsPlatformAdmin() {
		return errAdminOnly
	}
	return store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		g, err := getGateway(ctx, q, id, true)
		if err != nil {
			return err
		}
		row, err := q.DeleteGatewayGrant(ctx, dbgen.DeleteGatewayGrantParams{ID: grantID, GatewayID: g.ID})
		if errors.Is(store.NotFound(err), store.ErrNotFound) {
			return errNoGrant
		} else if err != nil {
			return err
		}
		subject, err := grantSubject(ctx, q, row)
		if err != nil {
			return err
		}
		e := a.Audit("gateway.grant_remove", "gateway_grant", row.ID.String())
		e.Before = map[string]any{"subjectKind": row.SubjectKind, "subject": subject}
		e.Metadata = map[string]any{"name": g.Name + " · " + subject, "gatewayId": g.ID.String(), "gatewayName": g.Name}
		return audit.Record(ctx, q, e)
	})
}

// grantSubject names a grant's subject as audited: a team's slug, a
// person's email or the group.
func grantSubject(ctx context.Context, q *dbgen.Queries, g dbgen.GatewayGrant) (string, error) {
	switch {
	case g.TeamID.Valid:
		t, err := q.GetTeamByID(ctx, g.TeamID.UUID)
		if err != nil {
			return "", err
		}
		return t.Slug, nil
	case g.UserID.Valid:
		u, err := q.GetUser(ctx, g.UserID.UUID)
		if err != nil {
			return "", err
		}
		return u.Email, nil
	case g.GroupName != nil:
		return *g.GroupName, nil
	}
	return "", nil
}
