/* The columns of a list of MCP servers (the team's and Admin's): name (with Sign-in per person for oauth servers), host, health, catalog state, what it offers. */
import type { ReactElement } from "react";
import { plural } from "../../lib/format";
import { timeColumn } from "../templates/list-page";
import { CellText, type DataTableColumn } from "@/components/ui/data-table/data-table";
import { TextLink } from "@/components/ui/text-link/text-link";
import s from "../../pages/shared.module.css";
import { EnabledBadge, HealthBadge, PendingChangesBadge, PublishStateBadge, SignInBadge } from "./badges";
import { type CapabilityCounts, countOf, countsText, healthLabels, kindLabels, kindOrder, type McpServer, publishStateLabels, urlHost } from "./labels";
import m from "./servers.module.css";

/** "2 tools · 1 prompt" for a table cell: on a narrow screen it wraps between the counts, not word by word. */
export function CountsCell({ counts }: { counts: CapabilityCounts }) {
  const kinds = kindOrder.filter((k) => countOf(counts, k) > 0);
  if (kinds.length === 0) return <>{countsText(counts)}</>;
  return (
    <>
      {kinds.map((k, i) => (
        <span key={k}>
          <span className={m.count}>
            {plural(countOf(counts, k), kindLabels[k].one, kindLabels[k].many)}
            {i < kinds.length - 1 ? " \u00b7" : ""}
          </span>
          {i < kinds.length - 1 ? " " : ""}
        </span>
      ))}
    </>
  );
}

/** What a server offers in a cell: the counts, "Not read yet", or that the last read failed. */
export function OffersCell({ server }: { server: Pick<McpServer, "counts" | "refreshedAt" | "lastRefreshError"> }) {
  if (!server.refreshedAt) return <span className={server.lastRefreshError ? m.danger : s.muted}>{server.lastRefreshError ? "Couldn't be read" : "Not read yet"}</span>;
  return (
    <>
      <CountsCell counts={server.counts} />
      {server.lastRefreshError && <span className={m.dangerSmall}>Last refresh failed</span>}
    </>
  );
}

export function serverColumns(link: (x: McpServer) => ReactElement, opts: { team?: boolean } = {}): DataTableColumn<McpServer>[] {
  return [
    {
      id: "name",
      header: "Server",
      accessor: (x) => x.name,
      rowHeader: true,
      hideable: false,
      sortable: true,
      cell: (x) => (
        <CellText
          primary={
            <span className={s.badges}>
              <TextLink render={link(x)}>{x.name}</TextLink>
              <SignInBadge authMode={x.authMode} />
            </span>
          }
          secondary={<span className={s.mono}>{x.slug}</span>}
        />
      ),
    },
    ...(opts.team
      ? [{ id: "team", header: "Team", accessor: (x: McpServer) => x.teamName, sortable: true, cell: (x: McpServer) => <CellText primary={x.teamName} secondary={<span className={s.mono}>{x.teamSlug}</span>} /> }]
      : []),
    { id: "host", header: "Host", accessor: (x) => urlHost(x.url), cell: (x) => <span className={m.host} title={x.url}>{urlHost(x.url)}</span>, defaultHiddenNarrow: true },
    {
      id: "health",
      header: "Health",
      accessor: (x) => healthLabels[x.health.status],
      sortable: true,
      cell: (x) => (x.enabled ? <HealthBadge health={x.health} stacked /> : <EnabledBadge enabled={false} />),
    },
    {
      id: "catalog",
      header: "Catalog",
      accessor: (x) => publishStateLabels[x.publishState],
      sortable: true,
      cell: (x) => (
        <span className={s.badges}>
          <PublishStateBadge state={x.publishState} />
          <PendingChangesBadge count={x.pendingChanges} />
        </span>
      ),
    },
    { id: "offers", header: "Offers", accessor: (x) => countsText(x.counts), cell: (x) => <OffersCell server={x} />, defaultHiddenNarrow: true },
    { ...timeColumn<McpServer>("updatedAt", "Updated", (x) => x.updatedAt), defaultHidden: true },
  ];
}
