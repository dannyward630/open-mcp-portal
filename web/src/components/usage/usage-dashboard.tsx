/*
 * A usage dashboard (Team › Usage and Admin › Usage): a range (presets or
 * whole UTC days, at most 90), a grouping and, in Admin, a team; totals
 * (calls, error rate, p50/p95 latency, bytes), calls per day or hour by
 * outcome, latency, and the grouped table. The choices live in the URL.
 * Two reports load: the chart's (by hour or day) and the table's (the chosen
 * grouping), one when they're the same.
 */
import { useQuery } from "@tanstack/react-query";
import { Activity, AlertTriangle, ArrowDownToLine, ArrowUpFromLine, Gauge, Timer } from "lucide-react";
import type { ReactNode } from "react";
import { api, unwrap } from "../../api/client";
import { useSearchParams } from "../../lib/url-search";
import { Alert, ErrorAlert } from "@/components/ui/alert/alert";
import { Card } from "@/components/ui/card/card";
import { EmptyState } from "@/components/ui/empty-state/empty-state";
import { SkeletonText } from "@/components/ui/skeleton/skeleton";
import { StatCard } from "@/components/ui/stat-card/stat-card";
import { Time } from "@/components/ui/time/time";
import s from "../../pages/shared.module.css";
import { UsageBreakdown } from "./breakdown";
import { CallsChart, LatencyChart } from "./charts";
import { UsageControls } from "./controls";
import { bucketFor, errorRate, type GroupBy, groupByLabels, groupByProblem, isPreset, ms, num, resolveRange, size, type UsageReport } from "./range";
import u from "./usage.module.css";

export type UsageScope = { kind: "team"; team: string } | { kind: "admin" };

/** The groupings each scope offers (teams group by gateway team only in Admin). */
export const groupings = (scope: UsageScope): GroupBy[] =>
  scope.kind === "admin" ? ["gateway", "server", "capability", "team", "principalKind", "day", "hour"] : ["gateway", "server", "capability", "principalKind", "day", "hour"];

type Query = { from: string; to: string; groupBy: GroupBy; team?: string };

function useReport(scope: UsageScope, q: Query | undefined) {
  return useQuery({
    queryKey: scope.kind === "team" ? ["team", scope.team, "usage", q] : ["admin", "usage", q],
    queryFn: async () => {
      const query = { ...q!, limit: 100 };
      return scope.kind === "team"
        ? unwrap(await api.GET("/v1/teams/{team}/usage", { params: { path: { team: scope.team }, query } }))
        : unwrap(await api.GET("/v1/admin/usage", { params: { query } }));
    },
    enabled: Boolean(q),
  });
}

export function UsageDashboard({ scope, teamOptions }: { scope: UsageScope; teamOptions?: { value: string; label: string }[] }) {
  const [params] = useSearchParams();
  const preset = isPreset(params.get("range")) ? (params.get("range") as Parameters<typeof resolveRange>[0]) : "7d";
  const allowed = groupings(scope);
  const groupBy = (allowed as string[]).includes(params.get("groupBy") ?? "") ? (params.get("groupBy") as GroupBy) : "gateway";
  const team = scope.kind === "admin" ? params.get("team") || undefined : undefined;
  const range = resolveRange(preset, params.get("from"), params.get("to"));
  const groupError = range.from ? groupByProblem(groupBy, range) : undefined;
  const problem = range.error ?? groupError;
  const ok = range.from && !problem ? range : undefined;
  const bucket = ok ? bucketFor(ok) : "day";
  const base = ok ? { from: ok.from.toISOString(), to: ok.to.toISOString(), ...(team ? { team } : {}) } : undefined;
  const timeline = useReport(scope, base && { ...base, groupBy: bucket });
  const sameAsChart = groupBy === bucket;
  const breakdown = useReport(scope, base && !sameAsChart ? { ...base, groupBy } : undefined);
  const table = sameAsChart ? timeline : breakdown;

  return (
    <>
      <UsageControls scope={scope} preset={preset} groupBy={groupBy} allowed={allowed} range={range.from ? range : undefined} teamOptions={teamOptions} rangeError={range.error} groupError={groupError} />
      {problem ? null : timeline.isLoading ? (
        <SkeletonText lines={6} />
      ) : !timeline.data ? (
        <ErrorAlert error={timeline.error} title="Couldn't load the usage" onRetry={() => void timeline.refetch()} />
      ) : (
        <Report report={timeline.data} bucket={bucket} groupBy={groupBy}>
          {table.isLoading ? (
            <SkeletonText lines={4} />
          ) : table.data ? (
            <>
              <UsageBreakdown report={table.data} groupBy={groupBy} admin={scope.kind === "admin"} />
              {table.data.truncated && <p className={s.note}>{`Only the ${num(table.data.rows.length)} with the most calls are listed; the totals above cover everything.`}</p>}
            </>
          ) : (
            <ErrorAlert error={table.error} title="Couldn't load the breakdown" onRetry={() => void table.refetch()} />
          )}
        </Report>
      )}
    </>
  );
}

function Report({ report, bucket, groupBy, children }: { report: UsageReport; bucket: "hour" | "day"; groupBy: GroupBy; children: ReactNode }) {
  const t = report.totals;
  const freshness = report.rolledUpTo ? (
    <p className={s.note}>
      Includes calls up to <Time value={report.rolledUpTo} format="datetime" />. Totals are updated every 5 minutes; times are UTC.
    </p>
  ) : (
    <Alert tone="info">Usage totals are computed every 5 minutes from the call records; none have been computed yet.</Alert>
  );
  if (t.calls === 0) {
    return (
      <>
        {freshness}
        <Card>
          <EmptyState icon={<Activity />} title="No calls in this range." description="Calls through gateways show up here within a few minutes. Try a longer range." />
        </Card>
      </>
    );
  }
  return (
    <>
      {freshness}
      <div className={s.stats3} role="group" aria-label="Totals for the range">
        <StatCard label="Calls" value={num(t.calls)} icon={<Activity />} hint={`${num(t.errors)} not OK`} />
        <StatCard label="Error rate" value={errorRate(t)} icon={<AlertTriangle />} />
        <StatCard label="p50 latency" value={ms(t.p50LatencyMs)} icon={<Timer />} />
        <StatCard label="p95 latency" value={ms(t.p95LatencyMs)} icon={<Gauge />} />
        <StatCard label="Sent to servers" value={size(t.requestBytes)} icon={<ArrowUpFromLine />} />
        <StatCard label="Received from servers" value={size(t.responseBytes)} icon={<ArrowDownToLine />} />
      </div>
      <div className={u.grid2}>
        <CallsChart report={report} bucket={bucket} />
        <LatencyChart report={report} bucket={bucket} />
      </div>
      <Card title={`By ${groupByLabels[groupBy].toLowerCase()}`} description={groupBy === "day" || groupBy === "hour" ? "In time order (UTC)." : "Most calls first."}>
        <div className={u.chart}>{children}</div>
      </Card>
    </>
  );
}
