/* Admin › Limits as a form: each key's default and ceiling as text, checks, and the changed keys as the update's items. */
import { ApiError, type Schemas } from "@/api/client";
import { parseRate, rateInput, rateProblem, type RateLimitKey } from "@/lib/limits";

export type PlatformLimit = Schemas["PlatformRateLimit"];
export type LimitRow = { def: string; ceil: string };
export type LimitsForm = Partial<Record<RateLimitKey, LimitRow>>;

export const limitsFormOf = (items: PlatformLimit[]): LimitsForm => Object.fromEntries(items.map((it) => [it.key, { def: rateInput(it.default), ceil: rateInput(it.ceiling) }]));

/** A row's problems: each value's own, and a default above the ceiling. */
export function rowErrors(it: PlatformLimit, f: LimitRow): { def?: string; ceil?: string } {
  const def = rateProblem(f.def);
  const ceil = it.teamOverridable ? rateProblem(f.ceil) : undefined;
  const d = parseRate(f.def);
  const c = it.teamOverridable ? parseRate(f.ceil) : null;
  if (!def && !ceil && c !== null && (d === null || d > c)) {
    return { def: d === null ? "Unlimited is above the ceiling: enter a default up to it, or remove the ceiling." : "The default can't be above the ceiling." };
  }
  return { def, ceil };
}

export const formInvalid = (items: PlatformLimit[], form: LimitsForm) => items.some((it) => Object.values(rowErrors(it, form[it.key] ?? { def: "", ceil: "" })).some(Boolean));

/** The keys whose values changed, as the update's items. */
export function limitChanges(items: PlatformLimit[], form: LimitsForm): Schemas["PlatformRateLimitSetting"][] {
  return items.flatMap((it) => {
    const f = form[it.key];
    if (!f) return [];
    const def = parseRate(f.def);
    const ceiling = it.teamOverridable ? parseRate(f.ceil) : null;
    return def === it.default && ceiling === it.ceiling ? [] : [{ key: it.key, default: def, ceiling }];
  });
}

/** null is unlimited: the tighter of two values. */
const tighter = (next: number | null, prev: number | null) => next !== null && (prev === null || next < prev);

/** Changes that make a limit stricter than before (asked about before saving). */
export function stricter(items: PlatformLimit[], changes: Schemas["PlatformRateLimitSetting"][]) {
  return changes.filter((c) => {
    const it = items.find((i) => i.key === c.key);
    return it && (tighter(c.default, it.default) || tighter(c.ceiling, it.ceiling));
  });
}

export const limitReasonLabels: Record<string, string> = {
  default_above_ceiling: "The default can't be above the ceiling.",
  not_overridable: "Teams can't override this limit, so it has no ceiling.",
  above_ceiling: "Above the ceiling.",
  duplicate: "Listed twice.",
};

/** A 422 invalid_limit as the key it's about and why. */
export function limitProblem(err: unknown): { key: string; message: string } | undefined {
  if (!(err instanceof ApiError) || err.code !== "invalid_limit" || typeof err.details?.key !== "string") return undefined;
  const reason = typeof err.details.reason === "string" ? err.details.reason : "";
  return { key: err.details.key, message: limitReasonLabels[reason] ?? err.message };
}
