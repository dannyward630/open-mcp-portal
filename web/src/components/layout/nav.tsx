/*
 * The sidebar's navigation, as data: the workspace's own pages, the active
 * team's pages and the admin portal's groups. The sidebar, the breadcrumbs
 * and the ⌘K palette all read these lists, so a page added here shows up in
 * all three.
 *
 * Adding a section (later phases: Gateways, Connect):
 *   1. add its route in router.tsx (the `to` paths below are typed against
 *      the route tree, so an item can't point at a page that doesn't exist);
 *   2. add an item to `workspaceNav` (pages for everyone: Catalog, Connect),
 *      `teamNav` (the team's own pages: MCP servers, Gateways, with an
 *      `access` rule) or a group of `adminSections` (catalog approvals,
 *      platform gateways);
 *   3. add its breadcrumb in breadcrumbs.tsx.
 * Only list pages that exist: no placeholders or "coming soon" links.
 */
import { BadgeCheck, FileClock, Home, LayoutDashboard, Library, Server, Settings, Users, UsersRound } from "lucide-react";
import { type ReactNode } from "react";
import { terms } from "../../lib/terms";
import { type Me } from "../../session";

export const icon = (C: typeof Home) => <C aria-hidden />;

/* ---------------- workspace ---------------- */

export type WorkspacePath = "/" | "/catalog";

export type WorkspaceNavItem = { to: WorkspacePath; label: string; icon: ReactNode; exact?: boolean; keywords?: string[] };

/** Pages for everyone signed in, at the top of the workspace sidebar. */
export const workspaceNav: WorkspaceNavItem[] = [
  { to: "/", label: terms.home, icon: icon(Home), exact: true, keywords: ["start", "welcome", "teams"] },
  { to: "/catalog", label: terms.catalog, icon: icon(Library), keywords: ["mcp", "servers", "published", "tools", "resources", "prompts", "browse"] },
];

/* ---------------- team ---------------- */

type Membership = Me["teams"][number];

/** Who may see what in a team (the same rules as the pages and the server). */
export type TeamAccess = {
  /** On the team (any role). */
  member: boolean;
  /** An active team's editors, admins and owners. */
  canEdit: boolean;
  /** An active team's admins and owners. */
  isManager: boolean;
  /** A platform admin or auditor (may view any team's overview, settings and MCP servers). */
  staff: boolean;
};

export function teamAccess(me: Me, mine: Membership | undefined): TeamAccess {
  const active = mine?.status === "active";
  const role = mine?.role;
  const isManager = active && (role === "owner" || role === "admin");
  return {
    member: Boolean(mine),
    canEdit: isManager || (active && role === "editor"),
    isManager,
    staff: me.capabilities.platformAdmin || me.capabilities.platformAuditor,
  };
}

export type TeamPath = "/teams/$team" | "/teams/$team/servers" | "/teams/$team/settings";

export type TeamNavItem = {
  to: TeamPath;
  label: string;
  icon: ReactNode;
  exact?: boolean;
  /** Who gets the item. Platform staff who aren't members get the overview, the MCP servers and settings. */
  access: (a: TeamAccess) => boolean;
  /** Extra command-palette search words. */
  keywords?: string[];
};

const teamNav: TeamNavItem[] = [
  { to: "/teams/$team", label: "Overview", icon: icon(LayoutDashboard), exact: true, access: (a) => a.member || a.staff, keywords: ["team", "dashboard"] },
  // Platform staff read any team's servers (the API allows it). Later team pages (Gateways) are members only.
  {
    to: "/teams/$team/servers",
    label: terms.mcpServers,
    icon: icon(Server),
    access: (a) => a.member || a.staff,
    keywords: ["mcp", "server", "servers", "register", "tools", "capabilities", "publish", "health"],
  },
  {
    to: "/teams/$team/settings",
    label: terms.teamSettings,
    icon: icon(Settings),
    access: (a) => a.member || a.staff,
    keywords: ["members", "invites", "audit", "general", "leave"],
  },
];

/** The team pages the user can open in the team at `slug`. */
export function teamNavFor(me: Me, slug: string): TeamNavItem[] {
  const a = teamAccess(
    me,
    me.teams.find((t) => t.slug === slug),
  );
  return teamNav.filter((item) => item.access(a));
}

/* ---------------- admin ---------------- */

export type AdminPath = "/admin/users" | "/admin/teams" | "/admin/servers" | "/admin/publishing" | "/admin/audit";

export type AdminNavItem = { to: AdminPath; label: string; icon: ReactNode; exact?: boolean };

/**
 * Admin sidebar groups. Later phases add groups here (Gateways: platform gateways; Settings), each with
 * the pages it holds.
 */
export const adminSections: { label?: string; items: AdminNavItem[] }[] = [
  {
    label: "People",
    items: [
      { to: "/admin/users", label: "Users", icon: icon(Users) },
      { to: "/admin/teams", label: "Teams", icon: icon(UsersRound) },
    ],
  },
  {
    label: "Catalog",
    items: [
      { to: "/admin/servers", label: terms.mcpServers, icon: icon(Server) },
      { to: "/admin/publishing", label: terms.publishing, icon: icon(BadgeCheck) },
    ],
  },
  {
    label: "Records",
    items: [{ to: "/admin/audit", label: terms.auditLog, icon: icon(FileClock) }],
  },
];

export const adminNav = adminSections.flatMap((section) => section.items);

/** The label of the admin group holding the page at `pathname`. */
export function activeAdminGroup(pathname: string): string | undefined {
  const inside = (to: string, exact?: boolean) => pathname === to || (!exact && pathname.startsWith(to + "/"));
  return adminSections.find((s) => s.items.some((i) => inside(i.to, i.exact)))?.label;
}

/** Extra command-palette search words for admin pages. */
export const adminKeywords: Partial<Record<AdminPath, string[]>> = {
  "/admin/users": ["people", "person", "suspend", "reactivate", "platform role", "platform admin", "auditor"],
  "/admin/teams": ["team", "create team", "archive", "owner", "assign owner"],
  "/admin/servers": ["mcp", "servers", "review", "approve", "reject", "pending", "catalog", "capabilities", "changes"],
  "/admin/publishing": ["policy", "approval required", "self publish", "default", "catalog"],
  "/admin/audit": ["audit log", "history", "changes", "who changed", "sign-ins", "export", "csv"],
};
