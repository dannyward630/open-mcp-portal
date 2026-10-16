package authz

import "github.com/ncecere/open-mcp-gateway/internal/apperr"

// Rules of gateways (docs/decisions.md, decisions 13, 16 and 17;
// docs/phase3-gateways.md): a team's owners and admins create, change and
// delete its gateways; members, and platform admins and auditors, read them.
// Platform admins manage platform gateways and their grants, and may only
// switch a team gateway off or on. A team gateway is used by the team's
// members, each capability optionally limited to a minimum team role; a
// platform gateway by the teams, people and SSO groups it is granted to.

// CheckGatewayEdit decides whether a team member with role may create,
// change or delete the team's gateways.
func CheckGatewayEdit(role string) error {
	if !RoleAtLeast(role, RoleAdmin) {
		return apperr.Forbidden("Only team owners and admins can change the team's gateways")
	}
	return nil
}

// MinRoleAllows reports whether a member with role may use a capability
// whose minimum role is min (nil: every member).
func MinRoleAllows(role string, min *string) bool {
	if min == nil {
		return ValidTeamRole(role)
	}
	return RoleAtLeast(role, *min)
}
