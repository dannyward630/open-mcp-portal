/*
 * Admin → Calls: every call through every gateway (metadata only), newest
 * first, with server-side filters in the URL: gateway, server, outcome,
 * person, team and date range. Platform admins and auditors.
 */
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { Activity } from "lucide-react";
import { api, unwrap } from "@/api/client";
import { callColumns, callsPrivacy, noCalls, outcomeFacet } from "@/components/gateways/calls-table";
import type { CallOutcome, McpCall } from "@/components/gateways/labels";
import { ListPage, useListFilters } from "@/components/templates/list-page";
import { Alert } from "@/components/ui/alert/alert";
import type { Facet, FacetOption } from "@/components/ui/filter-bar/filter-bar";
import { terms } from "@/lib/terms";
import { dateFacet, one, rangeWindow, usePeopleOptions } from "../audit/common";
import { useTeamOptions } from "./list";

/** Options from an admin list (the first 200), by ID. */
function useOptions(kind: "gateways" | "servers"): FacetOption[] {
  const list = useQuery({
    queryKey: ["admin", kind, "call-filter"],
    queryFn: async () =>
      kind === "gateways"
        ? unwrap(await api.GET("/v1/admin/gateways", { params: { query: { limit: 200 } } })).items.map((g) => ({ value: g.id, label: g.teamName ? `${g.name} (${g.teamName})` : g.name }))
        : unwrap(await api.GET("/v1/admin/mcp-servers", { params: { query: { limit: 200 } } })).items.map((s) => ({ value: s.id, label: `${s.name} (${s.teamName})` })),
    staleTime: 60_000,
  });
  return list.data ?? [];
}

function useFacets(): Facet<McpCall>[] {
  const gateways = useOptions("gateways");
  const servers = useOptions("servers");
  const people = usePeopleOptions();
  const teams = useTeamOptions();
  return [
    { id: "gateway", label: "Gateway", type: "select", placeholder: "Any gateway", options: gateways },
    { id: "server", label: "Server", type: "select", placeholder: "Any server", options: servers },
    outcomeFacet,
    { id: "person", label: "Person", type: "select", placeholder: "Anyone", options: people },
    { id: "team", label: "Team", type: "select", placeholder: "Any team", options: teams.data ?? [] },
    dateFacet<McpCall>(),
  ];
}

export function AdminCallsPage() {
  const facets = useFacets();
  const { values } = useListFilters(facets);
  const query = {
    gateway: one(values, "gateway") || undefined,
    server: one(values, "server") || undefined,
    outcome: (one(values, "outcome") || undefined) as CallOutcome | undefined,
    user: one(values, "person") || undefined,
    team: one(values, "team") || undefined,
    ...rangeWindow(values),
  };
  const calls = useInfiniteQuery({
    queryKey: ["admin", "calls", query],
    queryFn: async ({ pageParam }) => unwrap(await api.GET("/v1/admin/calls", { params: { query: { ...query, cursor: pageParam, limit: 50 } } })),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.nextCursor ?? undefined,
  });
  const items = calls.data?.pages.flatMap((p) => p.items) ?? [];
  return (
    <ListPage<McpCall>
      id="admin-calls"
      title={terms.calls}
      description="Every call through every gateway: who called which tool or prompt, through which gateway, and how it went."
      notices={<Alert tone="info">{callsPrivacy}</Alert>}
      caption={terms.calls}
      columns={callColumns({ gateway: true, adminLinks: true })}
      data={items}
      getRowId={(c) => String(c.id)}
      rowLabel={(c) => `${c.exposedName ?? c.method} by ${c.principal.displayName || c.principal.email}`}
      facets={facets}
      manual
      loading={calls.isLoading}
      error={calls.error}
      onRetry={() => void calls.refetch()}
      empty={{ icon: <Activity />, title: "No calls yet.", description: "Calls show up here once people use gateways from their MCP clients." }}
      tableProps={{
        noResults: noCalls,
        facetCounts: false,
        loadMore: { hasMore: Boolean(calls.hasNextPage), loading: calls.isFetchingNextPage, onLoadMore: () => void calls.fetchNextPage() },
      }}
    />
  );
}
