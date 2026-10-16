/*
 * A team's gateways: a ListPage (search in ?q=, filtered by the server)
 * with each gateway's switch, how many capabilities it offers and how many
 * it can't serve right now. Owners and admins create gateways on a form page
 * (?form=new); members and platform staff read.
 */
import { useInfiniteQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Network, Plus } from "lucide-react";
import { api, unwrap } from "../../../api/client";
import { teamGatewaysKey } from "../../../api/queries";
import { CreateGatewayForm } from "../../../components/gateways/create-gateway-form";
import { gatewayColumns } from "../../../components/gateways/gateway-columns";
import { type Gateway, whatIsAGateway } from "../../../components/gateways/labels";
import { useFormParam } from "../../../components/templates/form-page";
import { ListPage, useListFilters } from "../../../components/templates/list-page";
import { terms } from "../../../lib/terms";
import { Button } from "@/components/ui/button/button";
import { NARROW_QUERY, useMediaQuery } from "@/lib/bitop-utils";
import { useDebounced } from "../../admin/hooks";
import { ReadOnlyNotice } from "../access";
import { useTeam } from "../common";
import { ArchivedNotice } from "../layout";

export function TeamGatewaysPage() {
  const { slug, team, isManager, archived } = useTeam();
  const form = useFormParam();
  const canCreate = isManager && !archived;
  const { query } = useListFilters();
  const narrow = useMediaQuery(NARROW_QUERY);
  const q = useDebounced(query.trim());
  const gateways = useInfiniteQuery({
    queryKey: [...teamGatewaysKey(slug), "list", q],
    queryFn: async ({ pageParam }) =>
      unwrap(await api.GET("/v1/teams/{team}/gateways", { params: { path: { team: slug }, query: { q: q || undefined, cursor: pageParam, limit: 50 } } })),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.nextCursor ?? undefined,
  });
  const items = gateways.data?.pages.flatMap((p) => p.items) ?? [];
  const link = (x: Gateway) => <Link to="/teams/$team/gateways/$gatewayId" params={{ team: slug, gatewayId: x.id }} />;
  const create = canCreate && (
    <Button onClick={() => form.open("new")}>
      <Plus aria-hidden /> Create gateway
    </Button>
  );
  return (
    <>
      <ListPage<Gateway>
        id="team-gateways"
        title={terms.gateways}
        description={`Curated sets of tools and prompts, each at one MCP URL, for the members of ${team.name}.`}
        primaryAction={create}
        notices={
          <>
            <ArchivedNotice>Its gateways can be viewed but not changed.</ArchivedNotice>
            <ReadOnlyNotice what="the team's gateways" managers change="create and change them" />
          </>
        }
        caption={terms.gateways}
        columns={gatewayColumns(link, { narrow })}
        data={items}
        getRowId={(x) => x.id}
        rowLabel={(x) => x.name}
        search={{ label: "Search gateways", placeholder: "Name or slug" }}
        manual
        loading={gateways.isLoading}
        error={gateways.error}
        onRetry={() => void gateways.refetch()}
        rowActions={(x) => [
          { label: "Open", render: link(x) },
          { label: "Capabilities", render: <Link to="/teams/$team/gateways/$gatewayId" params={{ team: slug, gatewayId: x.id }} search={{ tab: "capabilities" }} /> },
          { label: "Settings", hidden: !canCreate, render: <Link to="/teams/$team/gateways/$gatewayId" params={{ team: slug, gatewayId: x.id }} search={{ tab: "settings" }} /> },
        ]}
        empty={q ? { icon: <Network />, title: "No gateways match." } : { icon: <Network />, title: "No gateways yet.", description: whatIsAGateway, action: create || undefined }}
        tableProps={{ loadMore: { hasMore: Boolean(gateways.hasNextPage), loading: gateways.isFetchingNextPage, onLoadMore: () => void gateways.fetchNextPage() } }}
      />
      {canCreate && form.id === "new" && <CreateGatewayForm scope={{ kind: "team", team: slug }} onClose={form.close} />}
    </>
  );
}
