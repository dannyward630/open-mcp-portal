/* API fixtures for the page tests, typed from the generated contract (schema.gen.ts), so they break when it changes. */
import type { Schemas } from "../api/client";

type PlatformRole = Schemas["PlatformRole"];
type TeamRole = Schemas["TeamRole"];

export const instance: Schemas["InstanceInfo"] = { name: "Open MCP Gateway", orgName: "", theme: "neutral", logoUrl: null, supportUrl: null };

export const authConfig = (extra: Partial<Schemas["AuthConfig"]> = {}): Schemas["AuthConfig"] => ({
  instance,
  oidcEnabled: false,
  devAuthEnabled: true,
  loginUrl: "/auth/login",
  teamRequestUrl: null,
  devAccounts: [
    { id: "admin", name: "Ada Admin", email: "admin@example.com", platformRole: "platform_admin" },
    { id: "auditor", name: "Avery Auditor", email: "auditor@example.com", platformRole: "platform_auditor" },
    { id: "alex", name: "Alex Owner", email: "alex@example.com", platformRole: "none" },
  ],
  ...extra,
});

export const team: Schemas["Team"] = {
  id: "11111111-1111-4111-8111-111111111111",
  slug: "platform-tools",
  name: "Platform Tools",
  description: "Internal MCP servers for developer tooling.",
  status: "active",
  revision: 3,
  createdAt: "2026-09-01T10:00:00Z",
  updatedAt: "2026-09-01T10:00:00Z",
  archivedAt: null,
};

export const myTeam = (role: TeamRole = "owner", extra: Partial<Schemas["MyTeam"]> = {}): Schemas["MyTeam"] => ({
  id: team.id,
  slug: team.slug,
  name: team.name,
  status: "active",
  role,
  ...extra,
});

export const user = (extra: Partial<Schemas["User"]> = {}): Schemas["User"] => ({
  id: "22222222-2222-4222-8222-222222222222",
  email: "una@example.com",
  displayName: "Una User",
  platformRole: "none",
  status: "active",
  revision: 1,
  createdAt: "2026-09-01T09:00:00Z",
  lastLoginAt: "2026-10-15T09:00:00Z",
  ...extra,
});

/** The signed-in user: a platform role and their teams (one team with `teamRole`, or none with null). */
export const meFor = (platformRole: PlatformRole = "none", teamRole: TeamRole | null = "owner"): Schemas["Me"] => ({
  user: user({ platformRole }),
  teams: teamRole ? [myTeam(teamRole)] : [],
  csrfToken: "csrf-123",
  capabilities: { platformAdmin: platformRole === "platform_admin", platformAuditor: platformRole === "platform_auditor" },
});

export const member = (role: TeamRole, extra: Partial<Schemas["MemberUser"]> = {}, revision = 1): Schemas["Member"] => ({
  user: { id: user().id, email: user().email, displayName: user().displayName, status: "active", ...extra },
  role,
  revision,
  createdAt: "2026-09-02T10:00:00Z",
});

export const members: Schemas["Member"][] = [
  member("owner"),
  member("admin", { id: "33333333-3333-4333-8333-333333333333", email: "blair@example.com", displayName: "Blair Admin" }),
  member("editor", { id: "44444444-4444-4444-8444-444444444444", email: "casey@example.com", displayName: "Casey Editor" }),
  member("member", { id: "55555555-5555-4555-8555-555555555555", email: "drew@example.com", displayName: "Drew Member" }),
];

export const invite = (extra: Partial<Schemas["Invite"]> = {}): Schemas["Invite"] => ({
  id: "66666666-6666-4666-8666-666666666666",
  email: "new.person@example.com",
  role: "editor",
  invitedBy: user().id,
  createdAt: "2026-10-10T10:00:00Z",
  expiresAt: "2026-11-09T10:00:00Z",
  ...extra,
});

export const teamSummary = (extra: Partial<Schemas["TeamSummary"]> = {}): Schemas["TeamSummary"] => ({
  team,
  memberCount: 4,
  ownerCount: 1,
  ownerInvites: 0,
  ...extra,
});

export const auditEntry = (extra: Partial<Schemas["AuditEntry"]> = {}): Schemas["AuditEntry"] => ({
  id: 101,
  occurredAt: "2026-10-15T12:00:00Z",
  actorKind: "user",
  actorUserId: user().id,
  teamId: team.id,
  teamName: team.name,
  teamSlug: team.slug,
  action: "team.member_role_change",
  actor: { kind: "user", userId: user().id, displayName: "Una User", email: "una@example.com" },
  targetType: "user",
  targetId: "44444444-4444-4444-8444-444444444444",
  targetLabel: "Casey Editor",
  targetExists: true,
  before: { role: "member" },
  after: { role: "editor" },
  metadata: {},
  requestId: "req-1",
  via: { kind: "session" },
  clientIp: null,
  ...extra,
});

export const page = <T>(items: T[], nextCursor: string | null = null) => ({ items, nextCursor });

/* ---------------- the registry (MCP servers, capabilities, catalog) ---------------- */

export const serverId = "99999999-9999-4999-8999-999999999999";

export const counts = (extra: Partial<Schemas["CapabilityCounts"]> = {}): Schemas["CapabilityCounts"] => ({ tools: 2, resources: 1, resourceTemplates: 0, prompts: 1, ...extra });

export const mcpServer = (extra: Partial<Schemas["McpServer"]> = {}): Schemas["McpServer"] => ({
  id: serverId,
  teamId: team.id,
  teamSlug: team.slug,
  teamName: team.name,
  slug: "service-status",
  name: "Service status",
  description: "Read the status of internal services.",
  url: "https://status.example.com/mcp",
  transport: "streamable_http",
  authHeaderName: "Authorization",
  hasAuth: true,
  authValueHint: "x9Kz",
  timeoutSeconds: 30,
  enabled: true,
  publishState: "private",
  effectivePublishPolicy: "approval_required",
  publishRequestedAt: null,
  publishRequestNote: null,
  publishReviewedAt: null,
  publishReviewedByName: null,
  publishReviewNote: null,
  counts: counts(),
  pendingChanges: 0,
  health: { status: "healthy", checkedAt: "2026-10-16T07:55:00Z", statusSince: "2026-10-15T08:00:00Z", latencyMs: 42 },
  refreshedAt: "2026-10-16T07:00:00Z",
  lastRefreshError: null,
  revision: 4,
  createdAt: "2026-10-01T10:00:00Z",
  updatedAt: "2026-10-16T07:00:00Z",
  ...extra,
});

export const capability = (extra: Partial<Schemas["McpCapability"]> = {}): Schemas["McpCapability"] => ({
  id: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
  serverId,
  kind: "tool",
  name: "get_status",
  title: "Get status",
  description: "Returns the status of a service.",
  definition: { name: "get_status", inputSchema: { type: "object", properties: { service: { type: "string" } } }, annotations: { readOnlyHint: true } },
  fingerprint: "a".repeat(64),
  approved: false,
  changed: false,
  approvedAt: null,
  approvedByName: null,
  firstSeenAt: "2026-10-01T10:00:00Z",
  lastSeenAt: "2026-10-16T07:00:00Z",
  goneAt: null,
  ...extra,
});

export const capabilities: Schemas["McpCapability"][] = [
  capability(),
  capability({ id: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", name: "restart", title: "", description: "Restarts a service.", definition: { name: "restart", annotations: { destructiveHint: true } } }),
  capability({ id: "cccccccc-cccc-4ccc-8ccc-cccccccccccc", kind: "resource", name: "status://all", title: "All services", description: "Every service's status.", definition: { uri: "status://all", mimeType: "application/json" } }),
  capability({ id: "dddddddd-dddd-4ddd-8ddd-dddddddddddd", kind: "prompt", name: "incident_summary", title: "Incident summary", description: "Summarise an incident.", definition: { name: "incident_summary" } }),
];

export const healthCheck = (extra: Partial<Schemas["McpHealthCheck"]> = {}): Schemas["McpHealthCheck"] => ({
  checkedAt: "2026-10-16T07:55:00Z",
  ok: true,
  latencyMs: 42,
  trigger: "scheduled",
  ...extra,
});

export const teamPublishing = (extra: Partial<Schemas["TeamPublishing"]> = {}): Schemas["TeamPublishing"] => ({
  override: null,
  effective: "approval_required",
  defaultPolicy: "approval_required",
  revision: team.revision,
  ...extra,
});

export const catalogServer = (extra: Partial<Schemas["CatalogServer"]> = {}): Schemas["CatalogServer"] => ({
  id: serverId,
  slug: "service-status",
  name: "Service status",
  description: "Read the status of internal services.",
  teamSlug: team.slug,
  teamName: team.name,
  counts: counts(),
  health: { status: "healthy", checkedAt: "2026-10-16T07:55:00Z", statusSince: "2026-10-15T08:00:00Z", latencyMs: 42 },
  publishedAt: "2026-10-10T10:00:00Z",
  ...extra,
});
