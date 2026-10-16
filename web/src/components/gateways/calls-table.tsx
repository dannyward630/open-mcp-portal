/*
 * Call records (metadata only: never arguments or results) as a list: when,
 * who and with which key, what was called, through which gateway to which
 * server, the outcome and how long it took. A gateway's Activity tab and
 * Admin › Calls share the columns and the outcome facet.
 */
import { Link } from "@tanstack/react-router";
import { Activity } from "lucide-react";
import { timeColumn } from "../templates/list-page";
import { CellText, type DataTableColumn } from "@/components/ui/data-table/data-table";
import { EmptyState } from "@/components/ui/empty-state/empty-state";
import type { Facet } from "@/components/ui/filter-bar/filter-bar";
import { TextLink } from "@/components/ui/text-link/text-link";
import s from "../../pages/shared.module.css";
import { OutcomeBadge } from "./badges";
import { callOutcomes, type McpCall, outcomeLabels } from "./labels";

/** The Outcome facet (one value: the API takes one). */
export const outcomeFacet: Facet<McpCall> = {
  id: "outcome",
  label: "Outcome",
  type: "select",
  placeholder: "Any outcome",
  options: callOutcomes.map((o) => ({ value: o, label: outcomeLabels[o] })),
};

const bytes = (n: number) => (n < 1024 ? `${n} B` : n < 1024 * 1024 ? `${(n / 1024).toFixed(1)} KiB` : `${(n / 1024 / 1024).toFixed(1)} MiB`);

/** What was called: the exposed tool or prompt name, or the method for a list call. */
const what = (c: McpCall) => c.exposedName ?? c.method;

type Opts = { gateway?: boolean; adminLinks?: boolean };

export function callColumns({ gateway = false, adminLinks = false }: Opts = {}): DataTableColumn<McpCall>[] {
  return [
    { ...timeColumn<McpCall>("occurredAt", "When", (c) => c.occurredAt), sortable: false },
    {
      id: "who",
      header: "Who",
      accessor: (c) => c.principal.displayName || c.principal.email,
      cell: (c) => (
        <CellText
          primary={<span title={c.principal.email}>{c.principal.displayName || c.principal.email}</span>}
          secondary={c.principal.apiKeyName ? `API key: ${c.principal.apiKeyName}` : undefined}
        />
      ),
    },
    {
      id: "what",
      header: "Called",
      accessor: what,
      rowHeader: true,
      cell: (c) => <CellText primary={<span className={s.mono}>{what(c)}</span>} secondary={c.exposedName ? c.method : undefined} />,
    },
    ...(gateway
      ? [
          {
            id: "gateway",
            header: "Gateway",
            accessor: (c: McpCall) => c.gateway.name,
            cell: (c: McpCall) =>
              adminLinks && c.gateway.exists ? (
                <TextLink render={<Link to="/admin/gateways/$gatewayId" params={{ gatewayId: c.gateway.id }} />}>{c.gateway.name}</TextLink>
              ) : (
                <span>
                  {c.gateway.name}
                  {!c.gateway.exists && <span className={s.secondary}>Deleted</span>}
                </span>
              ),
          },
        ]
      : []),
    {
      id: "server",
      header: "Server",
      accessor: (c) => c.server?.name ?? "",
      defaultHiddenNarrow: true,
      cell: (c) =>
        c.server ? (
          <span>
            {c.server.name}
            {!c.server.exists && <span className={s.secondary}>Deleted</span>}
          </span>
        ) : (
          <span className={s.muted}>—</span>
        ),
    },
    {
      id: "outcome",
      header: "Outcome",
      accessor: (c) => outcomeLabels[c.outcome],
      cell: (c) => (
        <span>
          <OutcomeBadge outcome={c.outcome} />
          {c.errorClass && <span className={s.secondary}>{c.errorClass}</span>}
        </span>
      ),
    },
    { id: "latency", header: "Time", accessor: (c) => c.latencyMs, cell: (c) => <span className={s.nowrap}>{c.latencyMs.toLocaleString()} ms</span>, defaultHiddenNarrow: true },
    { id: "size", header: "Size", accessor: (c) => c.requestBytes + c.responseBytes, defaultHidden: true, cell: (c) => <span className={s.nowrap}>{`${bytes(c.requestBytes)} in · ${bytes(c.responseBytes)} out`}</span> },
    { id: "client", header: "Client", accessor: (c) => c.clientName ?? "", defaultHidden: true, cell: (c) => c.clientName ?? <span className={s.muted}>—</span> },
    { id: "request", header: "Request ID", accessor: "requestId", muted: true, defaultHidden: true, cell: (c) => <span className={s.mono}>{c.requestId}</span> },
  ];
}

export const noCalls = <EmptyState size="compact" icon={<Activity />} title="No calls match these filters." description="Clear a filter to see more." />;

/** Call metadata only: said once above every call list. */
export const callsPrivacy = "Only metadata is recorded: who called what, when, and how it went. Arguments and results are never stored.";
