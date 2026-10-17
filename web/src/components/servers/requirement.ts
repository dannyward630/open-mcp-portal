/*
 * A server's user-authorization requirement (decision 26): the provider and
 * scopes whose user token the portal sends with calls to the server's
 * capabilities, and each capability's override (inherit, none, or other
 * scopes of the same provider). The rules mirror the server's
 * (api/openapi.yaml); the server still decides.
 *
 * Widening: under approval_required, a published server goes back to review
 * when any capability's effective requirement widens: a provider where there
 * was none, another provider, or a scope that wasn't in effect before.
 * Removing or narrowing never needs review. The forms warn before saving.
 */
import type { AuthProviderSummary, RequirementOverride } from "../providers/labels";
import type { McpCapability, McpServer } from "./labels";

/** The requirement as edited in Settings. */
export type RequirementForm = { on: boolean; providerId: string; scopes: string[] };

export function requirementFormOf(server: Pick<McpServer, "requirement">): RequirementForm {
  const r = server.requirement;
  return r ? { on: true, providerId: r.providerId, scopes: r.scopes } : { on: false, providerId: "", scopes: [] };
}

/** The PATCH's requirement: undefined keeps it, null removes it. */
export function requirementBody(f: RequirementForm, server: Pick<McpServer, "requirement">) {
  const saved = requirementFormOf(server);
  if (JSON.stringify(f.on ? f : { on: false }) === JSON.stringify(saved.on ? saved : { on: false })) return undefined;
  return f.on ? { providerId: f.providerId, scopes: f.scopes } : null;
}

/** An effective requirement: what a capability's calls carry. */
export type Effective = { providerId: string; scopes: string[] } | null;

/** The scopes in effect: the ones named, or the provider's defaults (empty when it isn't listed, such as a turned-off one). */
export function effectiveScopes(providerId: string, scopes: string[], providers: AuthProviderSummary[] = []) {
  return scopes.length ? scopes : (providers.find((p) => p.id === providerId)?.defaultScopes ?? []);
}

/** The server's own requirement in effect (the form's, when it's being edited). */
export function serverEffective(f: RequirementForm, providers?: AuthProviderSummary[]): Effective {
  return f.on && f.providerId ? { providerId: f.providerId, scopes: effectiveScopes(f.providerId, f.scopes, providers) } : null;
}

/** A provider where there was none, another provider, or a scope not in effect before. */
export function widens(before: Effective, after: Effective) {
  if (!after) return false;
  if (!before) return true;
  if (before.providerId !== after.providerId) return true;
  return after.scopes.some((s) => !before.scopes.includes(s));
}

const now = (c: Pick<McpCapability, "requires">): Effective => (c.requires ? { providerId: c.requires.providerId, scopes: c.requires.scopes } : null);

/** A capability's requirement once the server's becomes `next` (scopes overrides reset to inherit when the provider changes or goes). */
export function afterServerChange(c: Pick<McpCapability, "requirementOverride">, next: Effective): Effective {
  const o = c.requirementOverride;
  if (o.mode === "none") return null;
  if (o.mode === "scopes" && next && o.scopes?.length) return { providerId: next.providerId, scopes: o.scopes };
  return next;
}

/** A scopes override goes back to inherit when the provider changes (the others stay). */
const inherit = (o: RequirementOverride): RequirementOverride => (o.mode === "scopes" ? { mode: "inherit" } : o);

/**
 * Whether saving the server's requirement as `f` widens what any capability receives. With the capabilities
 * loaded, capability by capability (a capability set to none never widens); otherwise by the server's own.
 */
export function serverChangeWidens(server: Pick<McpServer, "requirement">, f: RequirementForm, caps: McpCapability[] | undefined, providers?: AuthProviderSummary[]) {
  const r = server.requirement;
  const before: Effective = r ? { providerId: r.providerId, scopes: effectiveScopes(r.providerId, r.scopes, providers) } : null;
  const after = serverEffective(f, providers);
  const live = (caps ?? []).filter((c) => !c.goneAt);
  if (live.length === 0) return widens(before, after);
  const providerChanged = before?.providerId !== after?.providerId;
  return live.some((c) => widens(now(c), afterServerChange({ requirementOverride: providerChanged ? inherit(c.requirementOverride) : c.requirementOverride }, after)));
}

/** What an override makes a capability receive, given the server's requirement. */
export function overrideEffective(o: RequirementOverride, server: Pick<McpServer, "requirement">, providers?: AuthProviderSummary[]): Effective {
  const r = server.requirement;
  if (!r || o.mode === "none") return null;
  if (o.mode === "scopes") return { providerId: r.providerId, scopes: o.scopes ?? [] };
  return { providerId: r.providerId, scopes: effectiveScopes(r.providerId, r.scopes, providers) };
}

/** Whether a change goes back to review: the server is published and its team needs approval. */
export const reviewedAgain = (server: Pick<McpServer, "publishState" | "effectivePublishPolicy">) =>
  server.publishState === "published" && server.effectivePublishPolicy === "approval_required";

/** "Inherits", "None", or the override's scopes, for a capability's row. */
export const overrideLabels: Record<RequirementOverride["mode"], string> = {
  inherit: "Same as the server",
  none: "No account",
  scopes: "Other scopes",
};
