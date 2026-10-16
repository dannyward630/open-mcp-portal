import { type Schemas } from "../api/client";

export type TeamRole = Schemas["TeamRole"];

export const teamRoles: TeamRole[] = ["owner", "admin", "editor", "member"];

export const roleLabels: Record<TeamRole, string> = { owner: "Owner", admin: "Admin", editor: "Editor", member: "Member" };

/** Mirrors the server rule: owners manage everyone; admins manage non-owners. */
export function canManage(actorRole: TeamRole | undefined, targetRole: TeamRole | "", newRole?: TeamRole) {
  if (actorRole === "owner") return true;
  if (actorRole !== "admin") return false;
  return targetRole !== "owner" && newRole !== "owner";
}

/**
 * What each team role can and can't do, for the role explanation (SPEC.md §4, the server's rules in internal/authz).
 * Short on purpose: the most asked-about abilities.
 */
export const roleAbilities: Record<TeamRole, { can: string[]; cannot: string[] }> = {
  owner: {
    can: ["Everything an admin can", "Add and remove other owners"],
    cannot: ["Create, rename or archive teams (platform admins do)"],
  },
  admin: {
    can: ["Everything an editor can", "Create, edit and delete the team's gateways", "Add, change and remove members (not owners)", "Invite people who haven't signed in yet", "Manage the team's settings"],
    cannot: ["Manage owners"],
  },
  editor: {
    can: ["Register and edit the team's MCP servers", "Read the team's audit log"],
    cannot: ["Create or change gateways", "Manage members or invites"],
  },
  member: {
    can: ["Use the gateways granted to them", "See the team's members"],
    cannot: ["Change the team's servers or gateways", "See the team's audit log"],
  },
};
