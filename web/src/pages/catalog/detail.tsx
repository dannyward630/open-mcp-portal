/*
 * A server in the catalog (/catalog/$serverId): who runs it, its health and
 * its approved capabilities grouped by kind, each with its definition. A
 * changed capability waits for approval and isn't listed here.
 */
import { useQuery } from "@tanstack/react-query";
import { Link, useParams } from "@tanstack/react-router";
import { ListTree, Server } from "lucide-react";
import { catalogServerQuery } from "../../api/queries";
import { isNotFound, NotFoundState } from "../../components/not-found";
import { PageSkeleton } from "../../components/page-skeleton";
import { HealthBadge } from "../../components/servers/badges";
import { CapabilityItem } from "../../components/servers/capability-item";
import { countsText, kindOrder, kindTitle, untrustedText } from "../../components/servers/labels";
import m from "../../components/servers/servers.module.css";
import { PageActions } from "../../components/templates/page-actions";
import { useCurrentUser } from "../../session";
import { Alert, ErrorAlert } from "@/components/ui/alert/alert";
import { Card } from "@/components/ui/card/card";
import { FactsLine } from "@/components/ui/description-list/description-list";
import { EmptyState } from "@/components/ui/empty-state/empty-state";
import { Stack } from "@/components/ui/layout/layout";
import { PageHeader } from "@/components/ui/page-header/page-header";
import { Time } from "@/components/ui/time/time";
import s from "../shared.module.css";
import { gatewaysNote } from "./list";

export function CatalogServerPage() {
  const { serverId } = useParams({ from: "/app/catalog/$serverId" });
  const me = useCurrentUser();
  const detail = useQuery(catalogServerQuery(serverId));
  if (detail.isLoading) return <PageSkeleton />;
  if (isNotFound(detail.error)) return <NotFoundState what="server" />;
  if (!detail.data) return <ErrorAlert error={detail.error} title="Couldn't load this server" onRetry={() => void detail.refetch()} />;
  const { server, capabilities } = detail.data;
  const mine = me.teams.some((t) => t.slug === server.teamSlug);
  return (
    <Stack gap={6} className={s.page}>
      <PageHeader
        title={server.name}
        meta={<HealthBadge health={server.health} />}
        description={server.description || undefined}
        facts={
          <FactsLine
            items={[
              { label: "Team", value: server.teamName },
              { label: "Published", value: <Time value={server.publishedAt} format="date" /> },
              { label: "Offers", value: countsText(server.counts) },
            ]}
          />
        }
        actions={
          mine ? (
            <PageActions
              secondary={[{ label: "Open in your team", icon: <Server aria-hidden />, render: <Link to="/teams/$team/servers/$serverId" params={{ team: server.teamSlug, serverId: server.id }} /> }]}
            />
          ) : undefined
        }
      />
      <Alert tone="info">{gatewaysNote}</Alert>
      {capabilities.length === 0 ? (
        <Card>
          <EmptyState icon={<ListTree />} title="No approved capabilities." description="The server is published, but nothing it lists is approved right now." />
        </Card>
      ) : (
        <>
          <p className={s.note}>{untrustedText}</p>
          {kindOrder
            .filter((k) => capabilities.some((c) => c.kind === k))
            .map((k) => {
              const items = capabilities.filter((c) => c.kind === k);
              return (
                <Card key={k} title={`${kindTitle(k)} (${items.length})`}>
                  <ul className={m.capList} aria-label={kindTitle(k)}>
                    {items.map((c) => (
                      <CapabilityItem key={c.id} cap={c} />
                    ))}
                  </ul>
                </Card>
              );
            })}
        </>
      )}
    </Stack>
  );
}
