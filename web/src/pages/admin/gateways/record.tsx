/* Admin → Gateways › one gateway: the shared record; platform gateways are edited here, team gateways only switched. */
import { useQuery } from "@tanstack/react-query";
import { useParams } from "@tanstack/react-router";
import { adminGatewayQuery } from "@/api/queries";
import { GatewayRecord, teamGatewayLink } from "@/components/gateways/gateway-record";
import { adminGatewayAbilities } from "@/components/gateways/scope";
import { isNotFound, NotFoundState } from "@/components/not-found";
import { PageSkeleton } from "@/components/page-skeleton";
import { Alert, ErrorAlert } from "@/components/ui/alert/alert";
import { useIsPlatformAdmin } from "../hooks";

export function AdminGatewayPage() {
  const { gatewayId } = useParams({ from: "/app/admin/gateways/$gatewayId" });
  const isAdmin = useIsPlatformAdmin();
  const gateway = useQuery(adminGatewayQuery(gatewayId));
  if (gateway.isLoading) return <PageSkeleton />;
  if (isNotFound(gateway.error)) return <NotFoundState what="gateway" />;
  if (!gateway.data) return <ErrorAlert error={gateway.error} title="Couldn't load this gateway" onRetry={() => void gateway.refetch()} />;
  const g = gateway.data;
  const team = teamGatewayLink(g);
  return (
    <GatewayRecord
      gateway={g}
      scope={{ kind: "admin" }}
      abilities={adminGatewayAbilities(isAdmin, g)}
      notices={
        g.ownerKind === "team" ? (
          <Alert tone="info">
            {`This gateway belongs to ${g.teamName ?? "a team"}: its owners and admins choose what it offers, and its members use it.${isAdmin ? " You can switch it off here." : ""}`}
          </Alert>
        ) : undefined
      }
      links={team ? [team] : []}
    />
  );
}
