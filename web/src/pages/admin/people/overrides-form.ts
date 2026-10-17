/*
 * A team's rate limit and retention overrides as one form (Admin › Teams › a
 * team › Limits & retention): each overridable key or kind inherits the
 * platform value or has the team's own. Checks, the effective value as
 * typed, and the changes as the two updates' items.
 */
import { ApiError, type Schemas } from "@/api/client";
import { parseRate, rateProblem, type RateLimitKey, type RateLimitValue } from "@/lib/limits";
import { daysProblem, type RetentionKind } from "@/lib/retention";

export type TeamLimit = Schemas["TeamRateLimit"];
export type TeamPeriod = Schemas["TeamRetentionPeriod"];
export type Mode = "inherit" | "custom";
export type OverrideRow = { mode: Mode; value: string };
/** One field per key and kind ("limit:gateway_per_minute", "period:call_metadata"), so a change made elsewhere to one doesn't overwrite another. */
export type OverridesForm = Partial<Record<`limit:${RateLimitKey}` | `period:${RetentionKind}`, OverrideRow>>;

export const limitField = (key: RateLimitKey) => `limit:${key}` as const;
export const periodField = (kind: RetentionKind) => `period:${kind}` as const;

const row = (v: number | null): OverrideRow => (v === null ? { mode: "inherit", value: "" } : { mode: "custom", value: String(v) });

export function overridesFormOf(limits: TeamLimit[], periods: TeamPeriod[]): OverridesForm {
  return Object.fromEntries([
    ...limits.filter((l) => l.teamOverridable).map((l) => [limitField(l.key), row(l.override)]),
    ...periods.map((p) => [periodField(p.kind), row(p.override)]),
  ]);
}

export const blank: OverrideRow = { mode: "inherit", value: "" };

/** A custom limit's problem: a whole number up to the ceiling (a team can't be made unlimited: inherit instead). */
export function limitError(l: TeamLimit, f: OverrideRow): string | undefined {
  if (f.mode === "inherit") return undefined;
  if (f.value.trim() === "") return "Enter a number of requests per minute, or inherit the default.";
  return rateProblem(f.value, l.ceiling);
}

export const periodError = (p: TeamPeriod, f: OverrideRow) => (f.mode === "inherit" ? undefined : daysProblem(f.value, p.minDays, p.maxDays));

/** What would apply with the form's value: the team's own, else the default, capped at the ceiling (null: unlimited). */
export function effectiveLimit(l: TeamLimit, f: OverrideRow): RateLimitValue {
  const v = f.mode === "inherit" || f.value.trim() === "" ? l.default : parseRate(f.value);
  if (v !== null && Number.isNaN(v)) return l.effective;
  if (l.ceiling === null) return v;
  return v === null ? l.ceiling : Math.min(v, l.ceiling);
}

export const effectivePeriod = (p: TeamPeriod, f: OverrideRow) => (f.mode === "inherit" || !/^\d+$/.test(f.value.trim()) ? p.default : Number(f.value));

/** The overrides that changed, as the updates' items (null: inherit). */
export function overrideChanges(limits: TeamLimit[], periods: TeamPeriod[], form: OverridesForm) {
  const limitItems = limits.flatMap((l) => {
    const f = form[limitField(l.key)];
    if (!l.teamOverridable || !f) return [];
    const value = f.mode === "inherit" ? null : parseRate(f.value);
    return value === l.override ? [] : [{ key: l.key, value }];
  });
  const periodItems = periods.flatMap((p) => {
    const f = form[periodField(p.kind)];
    if (!f) return [];
    const days = f.mode === "inherit" ? null : Number(f.value.trim());
    return days === p.override ? [] : [{ kind: p.kind, days }];
  });
  return { limits: limitItems, periods: periodItems };
}

export function overridesInvalid(limits: TeamLimit[], periods: TeamPeriod[], form: OverridesForm) {
  return (
    limits.some((l) => l.teamOverridable && Boolean(limitError(l, form[limitField(l.key)] ?? blank))) ||
    periods.some((p) => Boolean(periodError(p, form[periodField(p.kind)] ?? blank)))
  );
}

/** The kinds whose effective period gets shorter (asked about before saving). */
export function shorterPeriods(periods: TeamPeriod[], form: OverridesForm) {
  return periods.filter((p) => effectivePeriod(p, form[periodField(p.kind)] ?? blank) < p.effective);
}

const reasons: Record<string, string> = {
  above_ceiling: "Above the platform's ceiling.",
  not_overridable: "Teams can't override this.",
  out_of_range: "Outside the allowed range.",
  duplicate: "Listed twice.",
};

/** A 422 invalid_limit (details.key) or invalid_period (details.kind) as the row it's about. */
export function refusedOverride(err: unknown): { id: string; message: string } | undefined {
  if (!(err instanceof ApiError) || (err.code !== "invalid_limit" && err.code !== "invalid_period")) return undefined;
  const id = err.details?.key ?? err.details?.kind;
  if (typeof id !== "string") return undefined;
  const reason = typeof err.details?.reason === "string" ? err.details.reason : "";
  return { id, message: reasons[reason] ?? err.message };
}
