/* Typed fixtures for v0.2's screens (overview and attention, notifications, automation tokens), from the generated contract. */
import type { Schemas } from "../api/client";
import { serverId, team } from "./fixtures";

export const teamRef: Schemas["TeamRef"] = { id: team.id, slug: team.slug, name: team.name };

/* ---------------- overview and attention ---------------- */

export const attentionItem = (extra: Partial<Schemas["AttentionItem"]> = {}): Schemas["AttentionItem"] => ({
  kind: "server_failing",
  severity: "warning",
  summary: "Service status has been failing since 2026-10-15T09:00:00Z",
  count: 1,
  at: "2026-10-15T09:00:00Z",
  subject: { id: serverId, name: "Service status", slug: "service-status" },
  team: teamRef,
  link: { type: "server", id: serverId, teamSlug: team.slug },
  ...extra,
});

export const overview = (extra: Partial<Schemas["AdminOverview"]> = {}): Schemas["AdminOverview"] => ({
  generatedAt: "2026-10-17T12:00:00Z",
  counts: { users: 42, teams: 6, servers: 19, publishedServers: 11, gateways: 8, serviceAccounts: 5, automationTokens: 3 },
  last24h: { calls: 1200, errors: 30, errorRate: 0.025, rateLimited: 4, rolledUpTo: "2026-10-17T11:55:00Z" },
  attention: [],
  totals: {},
  truncated: false,
  ...extra,
});

export const teamAttention = (extra: Partial<Schemas["TeamAttention"]> = {}): Schemas["TeamAttention"] => ({
  generatedAt: "2026-10-17T12:00:00Z",
  attention: [],
  totals: {},
  truncated: false,
  ...extra,
});

/* ---------------- notifications ---------------- */

export const notification = (extra: Partial<Schemas["NotificationItem"]> = {}): Schemas["NotificationItem"] => ({
  id: 501,
  event: "server.unhealthy",
  title: "Service status is failing",
  body: "The last 3 health checks failed (connection refused).",
  team: teamRef,
  link: { type: "server", id: serverId, teamSlug: team.slug },
  createdAt: "2026-10-17T11:30:00Z",
  readAt: null,
  ...extra,
});

export const notificationPage = (items: Schemas["NotificationItem"][], unreadCount = items.filter((n) => !n.readAt).length, nextCursor: string | null = null): Schemas["NotificationPage"] => ({
  items,
  unreadCount,
  nextCursor,
});

export const preferences = (extra: Partial<Schemas["NotificationPreferences"]> = {}): Schemas["NotificationPreferences"] => ({
  emailAvailable: true,
  emailAddress: "una@example.com",
  items: [
    { event: "server.unhealthy", inApp: true, email: true, custom: false },
    { event: "server.recovered", inApp: true, email: true, custom: false },
    { event: "capability.changed", inApp: true, email: false, custom: true },
    { event: "publish.decided", inApp: true, email: true, custom: false },
    { event: "upstream_signin.lapsed", inApp: true, email: true, custom: false },
    { event: "key.expiring", inApp: true, email: true, custom: false },
    { event: "connection.needs_reconsent", inApp: true, email: true, custom: false },
  ],
  ...extra,
});

export const destination = (extra: Partial<Schemas["NotificationDestination"]> = {}): Schemas["NotificationDestination"] => ({
  id: "77777777-7777-4777-8777-777777777777",
  scope: "team",
  team: teamRef,
  name: "On-call webhook",
  format: "json",
  urlDisplay: "https://hooks.example.com/...",
  events: ["server.unhealthy", "server.recovered"],
  teamEvents: false,
  enabled: true,
  disabledReason: null,
  hasSecret: true,
  secretHint: "Zx9k",
  secretRotatedAt: null,
  previousSecretExpiresAt: null,
  health: { state: "ok", consecutiveFailures: 0, lastSuccessAt: "2026-10-17T11:00:00Z", lastFailureAt: null, lastErrorClass: null },
  revision: 1,
  createdAt: "2026-10-10T10:00:00Z",
  updatedAt: "2026-10-10T10:00:00Z",
  createdByName: "Una User",
  ...extra,
});

export const delivery = (extra: Partial<Schemas["NotificationDelivery"]> = {}): Schemas["NotificationDelivery"] => ({
  id: 9001,
  eventId: "88888888-8888-4888-8888-888888888888",
  event: "server.unhealthy",
  state: "sent",
  attempts: 1,
  createdAt: "2026-10-17T11:00:00Z",
  lastAttemptAt: "2026-10-17T11:00:01Z",
  nextAttemptAt: null,
  sentAt: "2026-10-17T11:00:01Z",
  httpStatus: 200,
  errorClass: null,
  ...extra,
});

export const notificationSettings = (extra: Partial<Schemas["NotificationSettings"]> = {}): Schemas["NotificationSettings"] => ({
  email: { configured: true, from: "Open MCP Portal <portal@example.org>", host: "smtp.example.org", port: 587, tls: "starttls", authenticated: true, sentLast24h: 12, failedLast24h: 0 },
  webhooks: { teamDestinations: 2, platformDestinations: 1, stopped: 0 },
  queue: { pending: 0, failedLast24h: 0, oldestPendingAt: null, lastRunAt: "2026-10-17T11:59:50Z", intervalSeconds: 15 },
  ...extra,
});

/* ---------------- automation tokens ---------------- */

export const automationToken = (extra: Partial<Schemas["AutomationToken"]> = {}): Schemas["AutomationToken"] => ({
  id: "99999999-0000-4000-8000-000000000001",
  kind: "team_publisher",
  name: "CI publisher",
  prefix: "omppub_Ab3dEf9h",
  scopes: ["servers:read", "servers:write"],
  status: "active",
  team: teamRef,
  createdAt: "2026-10-01T10:00:00Z",
  createdByName: "Una User",
  expiresAt: "2026-12-30T10:00:00Z",
  lastUsedAt: "2026-10-17T10:00:00Z",
  revokedAt: null,
  blockedReason: null,
  ...extra,
});

export const platformToken = (extra: Partial<Schemas["AutomationToken"]> = {}): Schemas["AutomationToken"] =>
  automationToken({ id: "99999999-0000-4000-8000-000000000002", kind: "platform_admin", name: "Monitoring", prefix: "ompadm_Qw2eRt5y", scopes: ["platform:read"], team: null, ...extra });

/** A secret as the API returns it once (43 characters after the prefix). */
export const tokenSecret = (prefix: "omppub_" | "ompadm_" = "omppub_") => `${prefix}${"s3cr3tS3cr3tS3cr3tS3cr3tS3cr3tS3cr3tS3cr3t".slice(0, 43)}`;
