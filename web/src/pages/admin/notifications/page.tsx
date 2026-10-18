/*
 * Admin › Notifications (/admin/notifications): is email set up (and a test email), is the delivery loop alive and
 * what failed, and the platform's webhook destinations (reviews waiting, platform tokens expiring and, when asked,
 * every team's events). Platform admins change things; auditors read.
 */
import { useQuery } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { DestinationsPanel } from "@/components/notifications/destinations/panel";
import { PageSkeleton } from "@/components/page-skeleton";
import { ErrorAlert } from "@/components/ui/alert/alert";
import { Stack } from "@/components/ui/layout/layout";
import { PageHeader } from "@/components/ui/page-header/page-header";
import { adminOnly, terms } from "@/lib/terms";
import s from "../../shared.module.css";
import { useIsPlatformAdmin } from "../hooks";
import { EmailCard } from "./email-card";
import { QueueCard } from "./queue-card";

export const notificationSettingsKey = ["admin", "notification-settings"];

export function AdminNotificationsPage() {
  const isAdmin = useIsPlatformAdmin();
  const settings = useQuery({
    queryKey: notificationSettingsKey,
    queryFn: async () => unwrap(await api.GET("/v1/admin/settings/notifications")),
    // The heartbeat and the queue move: look again every minute while the page is open.
    refetchInterval: 60_000,
  });
  return (
    <Stack gap={6} className={s.page}>
      <PageHeader
        title={terms.notifications}
        description={`Email, the delivery loop and the platform's webhooks. ${isAdmin ? "" : adminOnly}`.trim()}
      />
      {settings.isLoading ? (
        <PageSkeleton />
      ) : settings.data ? (
        <>
          <EmailCard email={settings.data.email} canTest={isAdmin} />
          <QueueCard settings={settings.data} />
        </>
      ) : (
        <ErrorAlert error={settings.error} title="Couldn't load the notification status" onRetry={() => void settings.refetch()} />
      )}
      <DestinationsPanel
        scope={{ kind: "platform" }}
        canManage={isAdmin}
        title="Platform webhooks"
        description="Messages for platform admins: servers waiting for review and platform tokens expiring. A destination can also take every team's events."
        readOnlyNote={isAdmin ? undefined : adminOnly}
      />
    </Stack>
  );
}
