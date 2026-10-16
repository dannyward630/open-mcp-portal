/*
 * A gateway's Activity tab: the calls through it (metadata only), newest
 * first, filtered by outcome and date (in the URL). Team gateways' calls are
 * for the team's owners and admins and platform staff; others are told who
 * can see them.
 */
import { useInfiniteQuery } from "@tanstack/react-query";
import { Activity, Lock } from "lucide-react";
import { api, unwrap } from "../../api/client";
import { adminGatewaysKey, teamGatewaysKey } from "../../api/queries";
import { rangeWindow } from "../../lib/date-window";
import { one } from "../../pages/admin/hooks";
import { ListPage, useListFilters } from "../templates/list-page";
import { Alert } from "@/components/ui/alert/alert";
import { Card } from "@/components/ui/card/card";
import { EmptyState } from "@/components/ui/empty-state/empty-state";
import type { Facet } from "@/components/ui/filter-bar/filter-bar";
import { callColumns, callsPrivacy, noCalls, outcomeFacet } from "./calls-table";
import type { CallOutcome, Gateway, McpCall } from "./labels";
import type { GatewayAbilities, GatewayScope } from "./scope";

const facets: Facet<McpCall>[] = [outcomeFacet, { id: "range", label: "Date", type: "date-range", pickerProps: { max: new Date() } }];

type Props = { gateway: Gateway; scope: GatewayScope; abilities: GatewayAbilities };

export function GatewayActivityTab({ gateway, scope, abilities }: Props) {
  if (!abilities.canReadCalls) {
    return (
      <Card>
        <EmptyState
          icon={<Lock />}
          title="Only the team's owners and admins see this gateway's calls."
          description="The call log shows who used which tool and when. Ask an owner or admin of the team if you need it."
        />
      </Card>
    );
  }
  return <CallList gateway={gateway} scope={scope} />;
}

function CallList({ gateway, scope }: { gateway: Gateway; scope: GatewayScope }) {
  const { values } = useListFilters(facets);
  const query = { gateway: gateway.id, outcome: (one(values, "outcome") || undefined) as CallOutcome | undefined, ...rangeWindow(values) };
  const team = gateway.teamSlug ?? (scope.kind === "team" ? scope.team : "");
  const calls = useInfiniteQuery({
    queryKey: [...(scope.kind === "team" ? teamGatewaysKey(scope.team) : adminGatewaysKey), gateway.id, "calls", query],
    queryFn: async ({ pageParam }) =>
      scope.kind === "team"
        ? unwrap(await api.GET("/v1/teams/{team}/calls", { params: { path: { team }, query: { ...query, cursor: pageParam, limit: 50 } } }))
        : unwrap(await api.GET("/v1/admin/calls", { params: { query: { ...query, cursor: pageParam, limit: 50 } } })),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.nextCursor ?? undefined,
  });
  const items = calls.data?.pages.flatMap((p) => p.items) ?? [];
  return (
    <>
      <Alert tone="info">{callsPrivacy}</Alert>
      <ListPage<McpCall>
        id="gateway-calls"
        caption="Calls"
        columns={callColumns()}
        data={items}
        getRowId={(c) => String(c.id)}
        rowLabel={(c) => `${c.exposedName ?? c.method} by ${c.principal.displayName || c.principal.email}`}
        facets={facets}
        manual
        loading={calls.isLoading}
        error={calls.error}
        onRetry={() => void calls.refetch()}
        empty={{ icon: <Activity />, title: "No calls yet.", description: "Calls show up here once someone uses the gateway from an MCP client." }}
        tableProps={{
          noResults: noCalls,
          facetCounts: false,
          loadMore: { hasMore: Boolean(calls.hasNextPage), loading: calls.isFetchingNextPage, onLoadMore: () => void calls.fetchNextPage() },
        }}
      />
    </>
  );
}
