/* One of the team's MCP servers (/teams/$team/servers/$serverId): the shared record, with the team's abilities. */
import { useQuery } from "@tanstack/react-query";
import { useParams } from "@tanstack/react-router";
import { teamServerQuery } from "../../../api/queries";
import { isNotFound, NotFoundState } from "../../../components/not-found";
import { PageSkeleton } from "../../../components/page-skeleton";
import { teamAbilities } from "../../../components/servers/scope";
import { adminLink, ServerRecord } from "../../../components/servers/server-record";
import { useCurrentUser } from "../../../session";
import { ErrorAlert } from "@/components/ui/alert/alert";
import { useTeam } from "../common";
import { ArchivedNotice } from "../layout";

export function TeamServerPage() {
  const { serverId } = useParams({ from: "/app/teams/$team/servers/$serverId" });
  const ctx = useTeam();
  const me = useCurrentUser();
  const server = useQuery(teamServerQuery(ctx.slug, serverId));
  if (server.isLoading) return <PageSkeleton />;
  if (isNotFound(server.error)) return <NotFoundState what="server" />;
  if (!server.data) return <ErrorAlert error={server.error} title="Couldn't load this server" onRetry={() => void server.refetch()} />;
  const staff = me.capabilities.platformAdmin || me.capabilities.platformAuditor;
  return (
    <ServerRecord
      server={server.data}
      scope={{ kind: "team", team: ctx.slug }}
      abilities={teamAbilities(ctx, server.data)}
      notices={<ArchivedNotice>Its servers can be viewed but not changed.</ArchivedNotice>}
      links={staff ? [adminLink(server.data)] : []}
    />
  );
}
