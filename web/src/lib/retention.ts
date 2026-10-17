/*
 * Retention (docs/phase6-operations.md "Retention"): the kinds' words, periods as text and the bounds check the forms
 * run before the server does.
 */
import { queryOptions } from "@tanstack/react-query";
import { api, type Schemas, unwrap } from "../api/client";
import { plural } from "./format";

export type RetentionKind = Schemas["RetentionKind"];

export const retentionKinds: RetentionKind[] = ["call_metadata", "captured_payloads", "audit_log", "usage_rollups"];

export const retentionLabels: Record<RetentionKind, { label: string; description: string }> = {
  call_metadata: { label: "Call records", description: "Who called what, when and how it went (never arguments or results)." },
  captured_payloads: { label: "Recorded call contents", description: "Arguments and results recorded by gateways and servers that record call contents." },
  audit_log: { label: "Audit log", description: "Every change and sign-in. Platform-wide only." },
  usage_rollups: { label: "Usage totals", description: "The hourly totals behind the usage dashboards. Platform-wide only." },
};

/** "1 day", "90 days". */
export const daysText = (n: number) => plural(n, "day");

/** What's wrong with a typed number of days, within the kind's bounds. */
export function daysProblem(text: string, min: number, max: number): string | undefined {
  const t = text.trim();
  if (!/^\d+$/.test(t)) return `Enter a whole number of days from ${min.toLocaleString()} to ${max.toLocaleString()}.`;
  const n = Number(t);
  if (n < min || n > max) return `Between ${min.toLocaleString()} and ${max.toLocaleString()} days.`;
  return undefined;
}

export const retentionSettingsKey = ["admin", "retention"];

export const retentionSettingsQuery = () =>
  queryOptions({ queryKey: retentionSettingsKey, queryFn: async () => unwrap(await api.GET("/v1/admin/settings/retention")) });

/** How long a team's call records and recorded contents are kept (members read it). */
export const teamRetentionQuery = (team: string) =>
  queryOptions({
    queryKey: ["team", team, "retention"],
    queryFn: async () => unwrap(await api.GET("/v1/teams/{team}/retention", { params: { path: { team } } })),
  });

export const adminTeamRetentionQuery = (team: string) =>
  queryOptions({
    queryKey: ["admin", "team", team, "retention"],
    queryFn: async () => unwrap(await api.GET("/v1/admin/teams/{team}/retention", { params: { path: { team } } })),
  });
