/*
 * The usage dashboards' time range and grouping (docs/phase6-operations.md "Usage"): presets of the last 24 hours to
 * 90 days, or whole UTC days; on the hour, at most 90 days, and at most 7 days when grouped by hour (the API's
 * rules, checked here so the person is told before asking).
 */
import type { Schemas } from "../../api/client";
import { formatDate } from "@/lib/bitop-format";

export type GroupBy = Schemas["UsageGroupBy"];
export type UsageReport = Schemas["UsageReport"];
export type UsageMetrics = Schemas["UsageMetrics"];
export type UsageRow = Schemas["UsageRow"];

export type Preset = "24h" | "7d" | "30d" | "90d" | "custom";

const HOUR = 3_600_000;
const DAY = 24 * HOUR;
export const maxDays = 90;
export const maxHourlyDays = 7;

export const presets: { value: Preset; label: string; hours: number }[] = [
  { value: "24h", label: "Last 24 hours", hours: 24 },
  { value: "7d", label: "Last 7 days", hours: 7 * 24 },
  { value: "30d", label: "Last 30 days", hours: 30 * 24 },
  { value: "90d", label: "Last 90 days", hours: 90 * 24 },
];

export const isPreset = (v: string | null): v is Preset => v === "custom" || presets.some((p) => p.value === v);

/** The start of the next UTC hour: the end of a preset range (the API's default "to"). */
export function nextHour(now: Date) {
  return new Date(Math.floor(now.getTime() / HOUR) * HOUR + HOUR);
}

/** "2026-10-16" as the start of that UTC day; null when it isn't a date. */
function utcDay(text: string | null): number | null {
  if (!text || !/^\d{4}-\d{2}-\d{2}$/.test(text)) return null;
  const t = Date.parse(`${text}T00:00:00Z`);
  return Number.isNaN(t) ? null : t;
}

/** A UTC date as YYYY-MM-DD (the custom range's inputs). */
export const isoDay = (d: Date) => d.toISOString().slice(0, 10);

export type Range = { from: Date; to: Date; error?: undefined } | { error: string; from?: undefined; to?: undefined };

/** The range the controls describe: a preset ending at the next hour, or whole UTC days (to inclusive). */
export function resolveRange(preset: Preset, fromDay: string | null, toDay: string | null, now = new Date()): Range {
  const found = presets.find((p) => p.value === preset);
  if (found) {
    const to = nextHour(now);
    return { from: new Date(to.getTime() - found.hours * HOUR), to };
  }
  const from = utcDay(fromDay);
  const last = utcDay(toDay);
  if (from === null || last === null) return { error: "Choose a start and an end date." };
  const to = last + DAY;
  if (from >= to) return { error: "Choose a start on or before the end." };
  if (to - from > maxDays * DAY) return { error: `Choose at most ${maxDays} days.` };
  return { from: new Date(from), to: new Date(to) };
}

export const spanDays = (r: { from: Date; to: Date }) => (r.to.getTime() - r.from.getTime()) / DAY;

/** Why a grouping can't be used for the range (hourly buckets only up to 7 days). */
export function groupByProblem(groupBy: GroupBy, r: { from: Date; to: Date }): string | undefined {
  return groupBy === "hour" && spanDays(r) > maxHourlyDays ? `Hourly totals cover at most ${maxHourlyDays} days. Choose a shorter range or group by day.` : undefined;
}

/** The chart's buckets: hours for up to two days, else days. */
export const bucketFor = (r: { from: Date; to: Date }): "hour" | "day" => (spanDays(r) <= 2 ? "hour" : "day");

export const groupByLabels: Record<GroupBy, string> = {
  gateway: "Gateway",
  server: "Server",
  capability: "Capability",
  team: "Team",
  principalKind: "Kind of caller",
  day: "Day",
  hour: "Hour",
};

export const principalKindLabels: Record<Schemas["UsagePrincipalKind"], string> = {
  api_key: "People with API keys",
  oauth: "People with connected apps",
  service_account: "Service accounts",
};

/** A bucket's start for people (UTC): "Oct 16" for a day, "Oct 16, 14:00" for an hour. */
export function bucketLabel(start: string, bucket: "hour" | "day") {
  return bucket === "day"
    ? formatDate(start, { style: "date", timeZone: "UTC", fallback: start })
    : formatDate(start, { style: "datetime", timeZone: "UTC", fallback: start });
}

/* ---------------- numbers ---------------- */

export const num = (v: number) => v.toLocaleString();
export const ms = (v: number | null | undefined) => (v === null || v === undefined ? "—" : v >= 1000 ? `${(v / 1000).toFixed(1)} s` : `${v.toLocaleString()} ms`);
export const errorRate = (m: Pick<UsageMetrics, "calls" | "errors">) => (m.calls === 0 ? "—" : `${(Math.round((m.errors / m.calls) * 1000) / 10).toLocaleString()}%`);
export function size(n: number) {
  if (n < 1024) return `${n.toLocaleString()} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KiB`;
  if (n < 1024 ** 3) return `${(n / 1024 / 1024).toFixed(1)} MiB`;
  return `${(n / 1024 ** 3).toFixed(2)} GiB`;
}
