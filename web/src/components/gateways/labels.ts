/*
 * The gateways' words (gateways, their capabilities, access and calls), shared by the team's pages, Admin,
 * My gateways and the Connect section.
 */
import type { Schemas } from "../../api/client";
import { plural } from "../../lib/format";

export type Gateway = Schemas["Gateway"];
export type GatewayCapability = Schemas["GatewayCapability"];
export type GatewayOption = Schemas["GatewayOption"];
export type GatewayGrant = Schemas["GatewayGrant"];
export type GatewayKind = Schemas["GatewayCapabilityKind"];
export type UnavailableReason = Schemas["GatewayUnavailableReason"];
export type MyGateway = Schemas["MyGateway"];
export type MyAccess = Schemas["MyGatewayAccess"];
export type McpCall = Schemas["McpCall"];
export type CallOutcome = Schemas["McpCallOutcome"];
export type ApiKey = Schemas["ApiKey"];
export type TeamRole = Schemas["TeamRole"];

/** What a gateway is, for empty lists and the create form. */
export const whatIsAGateway =
  "A gateway is a curated set of tools and prompts at one MCP address. Pick capabilities from your team's servers and the company catalog; the team's members connect their AI clients to the gateway's URL with an API key and see only what it offers.";

/** The same for platform gateways (Admin). */
export const whatIsAPlatformGateway =
  "A platform gateway is a curated set of approved tools and prompts from the catalog at one MCP address, shared with the teams, people or SSO groups you grant it to.";

/** The most capabilities one gateway can hold (the API's maxItems). */
export const maxCapabilities = 200;

export const gatewayKinds: GatewayKind[] = ["tool", "prompt"];

export const gatewayKindLabels: Record<GatewayKind, { one: string; many: string; title: string }> = {
  tool: { one: "tool", many: "tools", title: "Tools" },
  prompt: { one: "prompt", many: "prompts", title: "Prompts" },
};

/** "3 capabilities". */
export const capabilityCount = (n: number) => plural(n, "capability", "capabilities");

/** Why a selected capability isn't served now. */
export const unavailableLabels: Record<UnavailableReason, string> = {
  server_disabled: "Server disabled",
  not_published: "Left the catalog",
  not_approved: "Not approved",
  changed: "Changed, waiting for approval",
  gone: "No longer listed",
};

export const unavailableHelp: Record<UnavailableReason, string> = {
  server_disabled: "Its server is turned off.",
  not_published: "Its server was taken off the company catalog.",
  not_approved: "Its current definition isn't approved for the catalog.",
  changed: "Its definition changed since it was approved; it's served again once the change is approved.",
  gone: "The server doesn't list it any more.",
};

/** Why the person may use a gateway. */
export const accessLabels: Record<MyAccess, string> = {
  team_member: "Team member",
  team_grant: "Granted to your team",
  user_grant: "Granted to you",
  group_grant: "Granted to your SSO group",
};

export const outcomeLabels: Record<CallOutcome, string> = {
  ok: "OK",
  tool_error: "Tool error",
  denied: "Denied",
  upstream_error: "Upstream error",
  timeout: "Timed out",
  too_large: "Too large",
  bad_request: "Bad request",
};

export const outcomeHelp: Record<CallOutcome, string> = {
  ok: "Answered.",
  tool_error: "The tool reported an error.",
  denied: "Refused by policy. The client was told it wasn't found.",
  upstream_error: "The server was unreachable, refused or answered something malformed.",
  timeout: "The server didn't answer in time.",
  too_large: "The request or response was over 1 MiB.",
  bad_request: "The client's request was invalid.",
};

export const outcomeTone: Record<CallOutcome, "success" | "warning" | "danger" | "neutral"> = {
  ok: "success",
  tool_error: "warning",
  denied: "neutral",
  upstream_error: "danger",
  timeout: "danger",
  too_large: "warning",
  bad_request: "warning",
};

export const callOutcomes = Object.keys(outcomeLabels) as CallOutcome[];

/** "Team gateway" or "Platform gateway". */
export const ownerKindLabels: Record<Gateway["ownerKind"], string> = { team: "Team gateway", platform: "Platform gateway" };

/** The minimum-role choices: "" is every member. */
export const minRoleLabels: Record<TeamRole | "", string> = {
  "": "Every member",
  member: "Every member",
  editor: "Editors and above",
  admin: "Admins and owners",
  owner: "Owners only",
};

/** Roles a capability can require, lowest first ("" = no minimum). */
export const minRoleChoices: (TeamRole | "")[] = ["", "editor", "admin", "owner"];
