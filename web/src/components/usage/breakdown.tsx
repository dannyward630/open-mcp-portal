/*
 * The usage breakdown: one row per gateway, server, capability, team, kind
 * of caller, day or hour, most calls first (time groupings in time order),
 * with calls, errors, latency and bytes. In Admin, gateways, servers and
 * teams link to their pages; deleted ones keep the name they had.
 */
import { Link } from "@tanstack/react-router";
import { Table, Td, Tr } from "@/components/ui/table/table";
import { TextLink } from "@/components/ui/text-link/text-link";
import s from "../../pages/shared.module.css";
import { bucketLabel, errorRate, type GroupBy, groupByLabels, ms, num, principalKindLabels, size, type UsageReport, type UsageRow } from "./range";

type Props = { report: UsageReport; groupBy: GroupBy; admin: boolean };

function Name({ row, groupBy, admin }: { row: UsageRow; groupBy: GroupBy; admin: boolean }) {
  if (groupBy === "day" || groupBy === "hour") return <time dateTime={row.start ?? row.key}>{bucketLabel(row.start ?? row.key, groupBy)}</time>;
  if (groupBy === "principalKind") return <>{row.principalKind ? principalKindLabels[row.principalKind] : row.label}</>;
  if (groupBy === "capability") {
    const cap = row.capability;
    return (
      <>
        <span className={`${s.primary} ${s.mono}`}>{cap?.exposedName ?? row.label}</span>
        {cap?.server && <span className={s.secondary}>{cap.server.name}</span>}
      </>
    );
  }
  const ref = row.ref;
  if (!ref || !ref.exists) {
    return (
      <>
        <span className={s.primary}>{row.label}</span>
        {ref && <span className={s.secondary}>Deleted</span>}
      </>
    );
  }
  if (!admin) return <span className={s.primary}>{row.label}</span>;
  const link =
    groupBy === "gateway" ? (
      <Link to="/admin/gateways/$gatewayId" params={{ gatewayId: ref.id }} />
    ) : groupBy === "server" ? (
      <Link to="/admin/servers/$serverId" params={{ serverId: ref.id }} />
    ) : (
      <Link to="/admin/teams/$team" params={{ team: ref.slug }} />
    );
  return <TextLink render={link}>{row.label}</TextLink>;
}

export function UsageBreakdown({ report, groupBy, admin }: Props) {
  const time = groupBy === "day" || groupBy === "hour";
  // Time groupings list every bucket; the table shows the ones with calls.
  const rows = time ? report.rows.filter((r) => r.metrics.calls > 0) : report.rows;
  return (
    <Table
      caption={`Usage by ${groupByLabels[groupBy].toLowerCase()}`}
      stack
      columns={[
        groupByLabels[groupBy],
        { label: "Calls", numeric: true },
        { label: "Not OK", numeric: true },
        { label: "Error rate", numeric: true },
        { label: "p50", numeric: true },
        { label: "p95", numeric: true },
        { label: "Sent", numeric: true },
        { label: "Received", numeric: true },
      ]}
    >
      {rows.map((r) => (
        <Tr key={r.key}>
          <Td>
            <Name row={r} groupBy={groupBy} admin={admin} />
          </Td>
          <Td numeric>{num(r.metrics.calls)}</Td>
          <Td numeric>{num(r.metrics.errors)}</Td>
          <Td numeric>{errorRate(r.metrics)}</Td>
          <Td numeric nowrap>
            {ms(r.metrics.p50LatencyMs)}
          </Td>
          <Td numeric nowrap>
            {ms(r.metrics.p95LatencyMs)}
          </Td>
          <Td numeric nowrap>
            {size(r.metrics.requestBytes)}
          </Td>
          <Td numeric nowrap>
            {size(r.metrics.responseBytes)}
          </Td>
        </Tr>
      ))}
    </Table>
  );
}
