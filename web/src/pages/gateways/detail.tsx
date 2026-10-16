/*
 * A gateway the person can use (/gateways/$gatewayId): why they have
 * access, the capabilities it offers them right now (their role and each
 * capability's availability already applied), and how to connect a client.
 */
import { useQuery } from "@tanstack/react-query";
import { Link, useParams } from "@tanstack/react-router";
import { ListTree, Network } from "lucide-react";
import { myGatewayQuery } from "../../api/queries";
import { CapabilityList } from "../../components/gateways/capability-list";
import { ConnectSection } from "../../components/gateways/connect";
import { capabilityCount } from "../../components/gateways/labels";
import { isNotFound, NotFoundState } from "../../components/not-found";
import { PageSkeleton } from "../../components/page-skeleton";
import { untrustedText } from "../../components/servers/labels";
import { PageActions } from "../../components/templates/page-actions";
import { useCurrentUser } from "../../session";
import { Alert, ErrorAlert } from "@/components/ui/alert/alert";
import { Card } from "@/components/ui/card/card";
import { FactsLine } from "@/components/ui/description-list/description-list";
import { EmptyState } from "@/components/ui/empty-state/empty-state";
import { Stack } from "@/components/ui/layout/layout";
import { PageHeader } from "@/components/ui/page-header/page-header";
import s from "../shared.module.css";
import { AccessBadges } from "./list";

export function MyGatewayPage() {
  const { gatewayId } = useParams({ from: "/app/gateways/$gatewayId" });
  const me = useCurrentUser();
  const detail = useQuery(myGatewayQuery(gatewayId));
  if (detail.isLoading) return <PageSkeleton />;
  if (isNotFound(detail.error)) return <NotFoundState what="gateway" />;
  if (!detail.data) return <ErrorAlert error={detail.error} title="Couldn't load this gateway" onRetry={() => void detail.refetch()} />;
  const { gateway, capabilities } = detail.data;
  const mine = gateway.teamSlug && me.teams.some((t) => t.slug === gateway.teamSlug);
  return (
    <Stack gap={6} className={s.page}>
      <PageHeader
        title={gateway.name}
        meta={<AccessBadges gateway={gateway} />}
        description={gateway.description || undefined}
        facts={
          <FactsLine
            items={[
              { label: "From", value: gateway.ownerKind === "team" ? (gateway.teamName ?? "Team") : "Platform" },
              { label: "Offers you", value: capabilityCount(capabilities.length) },
            ]}
          />
        }
        actions={
          mine && gateway.teamSlug ? (
            <PageActions
              secondary={[{ label: "Open in your team", icon: <Network aria-hidden />, render: <Link to="/teams/$team/gateways/$gatewayId" params={{ team: gateway.teamSlug, gatewayId: gateway.id }} /> }]}
            />
          ) : undefined
        }
      />
      <ConnectSection slug={gateway.slug} url={gateway.url} />
      <Card title="What it offers you" description="Clients see each one as server__name. Only what you may use right now is listed.">
        {capabilities.length === 0 ? (
          <EmptyState
            size="compact"
            icon={<ListTree />}
            title="Nothing for you right now."
            description="The gateway offers you no capabilities at the moment: none are selected, they need a higher team role, or their servers are unavailable."
          />
        ) : (
          <Stack gap={4}>
            <Alert tone="info">{untrustedText}</Alert>
            <CapabilityList items={capabilities} availability={false} />
          </Stack>
        )}
      </Card>
    </Stack>
  );
}
