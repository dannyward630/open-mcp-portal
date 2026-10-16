/*
 * Queries shared by the app shell (breadcrumbs, the team switcher) and the
 * pages. They live here, not in the page modules, so the shell doesn't pull
 * lazily loaded pages into the main bundle.
 */
import { queryOptions } from "@tanstack/react-query";
import { api, unwrap } from "./client";

/** The team view (team + the viewer's role). Shared by the team layout and the breadcrumbs. */
export const teamQuery = (team: string) =>
  queryOptions({
    queryKey: ["team", team],
    queryFn: async () => unwrap(await api.GET("/v1/teams/{team}", { params: { path: { team } } })),
  });

export const membersKey = (team: string) => ["team", team, "members"];

/** A team's members, owners first. */
export const membersQuery = (team: string) =>
  queryOptions({
    queryKey: membersKey(team),
    queryFn: async () => unwrap(await api.GET("/v1/teams/{team}/members", { params: { path: { team } } })),
  });

/** One user with their memberships (admin). */
export const adminUserQuery = (userId: string) =>
  queryOptions({
    queryKey: ["admin", "user", userId],
    queryFn: async () => unwrap(await api.GET("/v1/admin/users/{userId}", { params: { path: { userId } } })),
  });

/* ---------------- MCP servers (the record pages and their breadcrumbs) ---------------- */

/** The query key prefix of a team's servers (lists, records and their capabilities). */
export const teamServersKey = (team: string) => ["team", team, "servers"];

/** One of a team's MCP servers (members and platform staff). */
export const teamServerQuery = (team: string, serverId: string) =>
  queryOptions({
    queryKey: [...teamServersKey(team), serverId],
    queryFn: async () => unwrap(await api.GET("/v1/teams/{team}/mcp-servers/{serverId}", { params: { path: { team, serverId } } })),
  });

/** The query key prefix of every team's servers in Admin. */
export const adminServersKey = ["admin", "servers"];

/** Any team's MCP server (platform admins and auditors). */
export const adminServerQuery = (serverId: string) =>
  queryOptions({
    queryKey: [...adminServersKey, serverId],
    queryFn: async () => unwrap(await api.GET("/v1/admin/mcp-servers/{serverId}", { params: { path: { serverId } } })),
  });

/** Servers waiting for review, counted up to a page ("200+" past it). Admin's MCP servers page and its sidebar item. */
export const pendingReviewsQuery = () =>
  queryOptions({
    queryKey: [...adminServersKey, "pending-count"],
    queryFn: async () => unwrap(await api.GET("/v1/admin/mcp-servers", { params: { query: { publishState: "pending", limit: 200 } } })),
    select: (p) => ({ count: p.items.length, more: Boolean(p.nextCursor) }),
  });

export const catalogKey = ["catalog"];

/** A published server and its approved capabilities (anyone signed in). */
export const catalogServerQuery = (serverId: string) =>
  queryOptions({
    queryKey: [...catalogKey, serverId],
    queryFn: async () => unwrap(await api.GET("/v1/catalog/servers/{serverId}", { params: { path: { serverId } } })),
  });

/** One team summary (admin). */
export const adminTeamQuery = (team: string) =>
  queryOptions({
    queryKey: ["admin", "team", team],
    queryFn: async () => unwrap(await api.GET("/v1/admin/teams/{team}", { params: { path: { team } } })),
  });

/* ---------------- gateways (the record pages and their breadcrumbs) ---------------- */

/** The query key prefix of a team's gateways (lists, records, their capabilities and calls). */
export const teamGatewaysKey = (team: string) => ["team", team, "gateways"];

/** One of a team's gateways (members and platform staff). */
export const teamGatewayQuery = (team: string, gatewayId: string) =>
  queryOptions({
    queryKey: [...teamGatewaysKey(team), gatewayId],
    queryFn: async () => unwrap(await api.GET("/v1/teams/{team}/gateways/{gatewayId}", { params: { path: { team, gatewayId } } })),
  });

/** The query key prefix of every gateway in Admin. */
export const adminGatewaysKey = ["admin", "gateways"];

/** Any gateway (platform admins and auditors). */
export const adminGatewayQuery = (gatewayId: string) =>
  queryOptions({
    queryKey: [...adminGatewaysKey, gatewayId],
    queryFn: async () => unwrap(await api.GET("/v1/admin/gateways/{gatewayId}", { params: { path: { gatewayId } } })),
  });

/** The query key prefix of the gateways the person can use (My gateways). Not "me": that key is the session's. */
export const myGatewaysKey = ["my", "gateways"];

/** A gateway the person can use, with what it offers them now. */
export const myGatewayQuery = (gatewayId: string) =>
  queryOptions({
    queryKey: [...myGatewaysKey, gatewayId],
    queryFn: async () => unwrap(await api.GET("/v1/me/gateways/{gatewayId}", { params: { path: { gatewayId } } })),
  });

/** Every gateway the person can use, and why. */
export const myGatewaysQuery = () =>
  queryOptions({
    queryKey: [...myGatewaysKey, "list"],
    queryFn: async () => unwrap(await api.GET("/v1/me/gateways")),
  });

/** The person's API keys. */
export const myApiKeysQuery = () =>
  queryOptions({
    queryKey: ["my", "api-keys"],
    queryFn: async () => unwrap(await api.GET("/v1/me/api-keys")),
  });
