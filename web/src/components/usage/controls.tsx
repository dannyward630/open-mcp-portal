/*
 * The usage dashboard's controls, kept in the URL (?range=, ?from=, ?to=,
 * ?groupBy=, Admin's ?team=): a preset or whole UTC days, the grouping
 * (hourly only for up to 7 days) and, in Admin, one team's scope.
 */
import { Field } from "@/components/ui/field/field";
import { Input, NativeSelect } from "@/components/ui/input/input";
import { useSearchParams } from "../../lib/url-search";
import { type GroupBy, groupByLabels, isoDay, maxDays, maxHourlyDays, type Preset, presets, spanDays } from "./range";
import type { UsageScope } from "./usage-dashboard";
import u from "./usage.module.css";

type Props = {
  scope: UsageScope;
  preset: Preset;
  groupBy: GroupBy;
  allowed: GroupBy[];
  /** The valid range shown now (undefined while the custom dates are invalid). */
  range?: { from: Date; to: Date };
  teamOptions?: { value: string; label: string }[];
  /** Why the custom dates can't be used. */
  rangeError?: string;
  /** Why the grouping can't be used with the range. */
  groupError?: string;
};

export function UsageControls({ scope, preset, groupBy, allowed, range, teamOptions, rangeError, groupError }: Props) {
  const [params, setParams] = useSearchParams();
  const set = (patch: Record<string, string | undefined>) =>
    setParams((p) => {
      const out = new URLSearchParams(p);
      for (const [k, v] of Object.entries(patch)) {
        if (v) out.set(k, v);
        else out.delete(k);
      }
      return out;
    });
  const hourlyTooLong = range ? spanDays(range) > maxHourlyDays : false;
  const lastDay = range ? isoDay(new Date(range.to.getTime() - 1)) : "";
  return (
    <div className={u.controls} role="group" aria-label="Usage range and grouping">
      <Field label="Range" className={u.control}>
        <NativeSelect
          value={preset}
          onChange={(e) => {
            const next = e.target.value as Preset;
            // Custom starts from the days shown now.
            set(next === "custom" ? { range: next, from: range ? isoDay(range.from) : undefined, to: lastDay || undefined } : { range: next, from: undefined, to: undefined });
          }}
        >
          {presets.map((p) => (
            <option key={p.value} value={p.value}>
              {p.label}
            </option>
          ))}
          <option value="custom">Custom dates (UTC)</option>
        </NativeSelect>
      </Field>
      {preset === "custom" && (
        <>
          <Field label="From (UTC)" className={u.control} description={`At most ${maxDays} days`}>
            <Input type="date" value={params.get("from") ?? ""} max={params.get("to") ?? undefined} onChange={(e) => set({ from: e.target.value })} />
          </Field>
          <Field label="To (UTC)" className={u.control} error={rangeError}>
            <Input type="date" value={params.get("to") ?? ""} min={params.get("from") ?? undefined} onChange={(e) => set({ to: e.target.value })} />
          </Field>
        </>
      )}
      <Field label="Group by" className={u.control} error={groupError} description={hourlyTooLong ? `Hour: ${maxHourlyDays} days at most` : undefined}>
        <NativeSelect value={groupBy} onChange={(e) => set({ groupBy: e.target.value })}>
          {allowed.map((g) => (
            <option key={g} value={g} disabled={g === "hour" && hourlyTooLong && groupBy !== "hour"}>
              {groupByLabels[g]}
            </option>
          ))}
        </NativeSelect>
      </Field>
      {scope.kind === "admin" && (
        <Field label="Team" className={u.controlWide} description="Its gateways, its servers and its service accounts">
          <NativeSelect value={params.get("team") ?? ""} onChange={(e) => set({ team: e.target.value || undefined })}>
            <option value="">Every team</option>
            {(teamOptions ?? []).map((t) => (
              <option key={t.value} value={t.value}>
                {t.label}
              </option>
            ))}
          </NativeSelect>
        </Field>
      )}
    </div>
  );
}
