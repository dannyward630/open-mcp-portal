/* One of the team's gateways (/teams/$team/gateways/$gatewayId): the shared record, with the team's abilities. */
import { useQuery } from "@tanstack/react-query";
import { useParams } from "@tanstack/react-router";
import { teamGatewayQuery } from "../../../api/queries";
import { adminGatewayLink, GatewayRecord } from "../../../components/gateways/gateway-record";
import { teamGatewayAbilities } from "../../../components/gateways/scope";
import { isNotFound, NotFoundState } from "../../../components/not-found";
import { PageSkeleton } from "../../../components/page-skeleton";
import { useCurrentUser } from "../../../session";
import { ErrorAlert } from "@/components/ui/alert/alert";
import { useTeam } from "../common";
import { ArchivedNotice } from "../layout";

export function TeamGatewayPage() {
  const { gatewayId } = useParams({ from: "/app/teams/$team/gateways/$gatewayId" });
  const ctx = useTeam();
  const me = useCurrentUser();
  const gateway = useQuery(teamGatewayQuery(ctx.slug, gatewayId));
  if (gateway.isLoading) return <PageSkeleton />;
  if (isNotFound(gateway.error)) return <NotFoundState what="gateway" />;
  if (!gateway.data) return <ErrorAlert error={gateway.error} title="Couldn't load this gateway" onRetry={() => void gateway.refetch()} />;
  const staff = me.capabilities.platformAdmin || me.capabilities.platformAuditor;
  return (
    <GatewayRecord
      gateway={gateway.data}
      scope={{ kind: "team", team: ctx.slug }}
      abilities={teamGatewayAbilities({ ...ctx, staff })}
      notices={<ArchivedNotice>Its gateways can be viewed but not changed.</ArchivedNotice>}
      links={staff ? [adminGatewayLink(gateway.data)] : []}
    />
  );
}
