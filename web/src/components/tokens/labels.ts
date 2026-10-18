/*
 * Words for automation tokens: the kinds, what each scope allows and, explicitly, what a token of each kind can never
 * do (docs/automation-tokens.md, "Never for tokens"). The "cannot" lists are part of the screen on purpose: people
 * decide what to hand to a pipeline by reading them.
 */
import type { AutomationToken, TokenScope, TokenScopeName } from "./api";

/** At most this many active tokens per team and for the platform. */
export const maxActiveTokens = 20;
export const defaultExpiryDays = 90;
export const maxExpiryDays = 365;

export const kindLabels: Record<AutomationToken["kind"], string> = { platform_admin: "Platform admin", team_publisher: "Team publisher" };
export const kindTone = { platform_admin: "warning", team_publisher: "info" } as const;

export const statusLabels = { active: "Active", expired: "Expired", revoked: "Revoked", blocked: "Blocked" } as const;
export const statusTone = { active: "success", expired: "neutral", revoked: "danger", blocked: "warning" } as const;

/** A token's state as shown: a platform token whose creator lost the admin role is refused, whatever its status says. */
export function displayStatus(t: AutomationToken): keyof typeof statusLabels {
  return t.status === "active" && t.blockedReason ? "blocked" : t.status;
}

export const blockedText = "Blocked: its creator is no longer a platform admin. It works again if they are restored.";

export type ScopeChoice = { value: TokenScopeName; label: string; can: string };

export const teamScopes: ScopeChoice[] = [
  {
    value: "servers:read",
    label: "Read servers",
    can: "List this team's MCP servers and read their capabilities, health checks and publishing state. Changes nothing.",
  },
  {
    value: "servers:write",
    label: "Read and manage servers",
    can: "Everything in read, plus register servers, change their settings, test and refresh them, detect their sign-in, and request or withdraw their publication (a server is published at once only where the portal's policy lets the team publish itself).",
  },
];

export const platformScopes: ScopeChoice[] = [
  {
    value: "platform:read",
    label: "Read the platform",
    can: "Read the admin API: users, teams, servers, gateways, the audit log, calls, usage, settings, the overview and the notification status. Never recorded call contents.",
  },
  {
    value: "platform:write",
    label: "Read and provision",
    can: "Everything in read, plus provisioning that can be undone: create and update teams and gateways, set a gateway's capabilities and grants, change rate limits, and revoke a person's connected account or app grant.",
  },
];

export const scopeChoices = (scope: TokenScope) => (scope.kind === "team" ? teamScopes : platformScopes);

/** Shown on every token's create dialog: what no token of this kind can do, whatever its scope. */
export const cannotTeam = [
  "Delete a server",
  "Approve capabilities or review a server: a person does that",
  "Change gateways, members, service accounts or any setting",
  "Switch payload capture on or off, or read recorded call contents",
  "Create or revoke tokens",
  "Reach another team, or use an MCP gateway (/mcp)",
];

export const cannotPlatform = [
  "Create or revoke tokens",
  "Change anyone's role, suspend people or assign team owners",
  "Review or approve servers and capabilities",
  "Change the publishing policy, internal networks, trusted clients, payload capture, retention or auth providers",
  "Delete anything",
  "Read recorded call contents, or use an MCP gateway (/mcp)",
];

export const cannotList = (scope: TokenScope) => (scope.kind === "team" ? cannotTeam : cannotPlatform);

/** The scopes a token holds that are worth listing: write includes read, so show the highest. */
export function shownScopes(t: AutomationToken): TokenScopeName[] {
  const write = t.scopes.find((s) => s.endsWith(":write"));
  return write ? [write] : t.scopes;
}

/** A small request that shows a token works, with a placeholder where the secret goes (never the secret itself). */
export function usageSnippet(scope: TokenScope, origin: string): string {
  const path = scope.kind === "team" ? `/v1/teams/${scope.team}/mcp-servers` : "/v1/admin/overview";
  return `curl -H "Authorization: Bearer $OMP_TOKEN" ${origin}${path}`;
}
