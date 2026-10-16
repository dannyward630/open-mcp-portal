/* Admin → MCP servers › one server: the shared record, read-only settings, and the review actions for platform admins. */
import { useQuery } from "@tanstack/react-query";
import { useParams } from "@tanstack/react-router";
import { adminServerQuery } from "@/api/queries";
import { isNotFound, NotFoundState } from "@/components/not-found";
import { PageSkeleton } from "@/components/page-skeleton";
import { adminAbilities } from "@/components/servers/scope";
import { ServerRecord, teamLink } from "@/components/servers/server-record";
import { ErrorAlert } from "@/components/ui/alert/alert";
import { useIsPlatformAdmin } from "../hooks";

export function AdminServerPage() {
  const { serverId } = useParams({ from: "/app/admin/servers/$serverId" });
  const isAdmin = useIsPlatformAdmin();
  const server = useQuery(adminServerQuery(serverId));
  if (server.isLoading) return <PageSkeleton />;
  if (isNotFound(server.error)) return <NotFoundState what="server" />;
  if (!server.data) return <ErrorAlert error={server.error} title="Couldn't load this server" onRetry={() => void server.refetch()} />;
  return <ServerRecord server={server.data} scope={{ kind: "admin" }} abilities={adminAbilities(isAdmin)} links={[teamLink(server.data)]} />;
}
