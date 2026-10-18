/*
 * The Inbox (/notifications), the bell's "View all": the person's notifications, newest first, in pages ("Load more"
 * keeps the cursor), All or Unread only (?unread=true). A row opens its target and marks itself read; Mark all read
 * clears the lot. Notifications are kept for 90 days, read or not.
 */
import { useInfiniteQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { BellOff, CheckCheck, Settings } from "lucide-react";
import { api, unwrap } from "../../api/client";
import { inboxKey } from "../../api/notifications";
import { NotificationRow } from "../../components/notifications/row";
import { useMarkRead } from "../../components/notifications/use-mark-read";
import { LoadMore } from "../../components/query-view";
import { PageActions } from "../../components/templates/page-actions";
import { terms } from "../../lib/terms";
import { useSearchParams } from "../../lib/url-search";
import { useCurrentUser } from "../../session";
import { ErrorAlert } from "@/components/ui/alert/alert";
import { Button } from "@/components/ui/button/button";
import { Card } from "@/components/ui/card/card";
import { EmptyState } from "@/components/ui/empty-state/empty-state";
import { Stack } from "@/components/ui/layout/layout";
import { PageHeader } from "@/components/ui/page-header/page-header";
import { Loading } from "@/components/ui/spinner/spinner";
import { Switch } from "@/components/ui/switch/switch";
import n from "../../components/notifications/notifications.module.css";
import s from "../shared.module.css";

const pageSize = 25;

export function InboxPage() {
  const me = useCurrentUser();
  const [params, setParams] = useSearchParams();
  const unreadOnly = params.get("unread") === "true";
  const setUnreadOnly = (on: boolean) =>
    setParams((p) => {
      const out = new URLSearchParams(p);
      if (on) out.set("unread", "true");
      else out.delete("unread");
      return out;
    });
  const list = useInfiniteQuery({
    queryKey: inboxKey(unreadOnly),
    queryFn: async ({ pageParam }) => unwrap(await api.GET("/v1/me/notifications", { params: { query: { unread: unreadOnly || undefined, cursor: pageParam, limit: pageSize } } })),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.nextCursor ?? undefined,
  });
  const { one, all } = useMarkRead();
  const items = list.data?.pages.flatMap((p) => p.items) ?? [];
  const unread = list.data?.pages[0]?.unreadCount ?? 0;
  return (
    <Stack gap={6} className={s.page}>
      <PageHeader
        title={terms.inbox}
        description="What needs you, newest first: failing servers, reviews, expiring keys. Kept for 90 days."
        actions={<PageActions secondary={[{ label: "Notification settings", icon: <Settings aria-hidden />, render: <Link to="/settings/notifications" /> }]} />}
      />
      <div className={n.inboxToolbar}>
        <Switch label="Unread only" checked={unreadOnly} onCheckedChange={setUnreadOnly} />
        <Button variant="secondary" size="sm" disabled={unread === 0} loading={all.isPending} onClick={() => all.mutate()}>
          <CheckCheck aria-hidden /> Mark all read{unread > 0 ? ` (${unread})` : ""}
        </Button>
      </div>
      <Card flush>
        {list.isLoading ? (
          <Loading label="Loading notifications…" />
        ) : list.error ? (
          <div className={s.pad}>
            <ErrorAlert error={list.error} title="Couldn't load your notifications" onRetry={() => void list.refetch()} />
          </div>
        ) : items.length === 0 ? (
          <EmptyState
            icon={<BellOff />}
            title={unreadOnly ? "Nothing unread." : "No notifications yet."}
            description={unreadOnly ? "You're all caught up." : "When a server fails or a key is about to expire, it shows up here. Choose what reaches you in Notification settings."}
          />
        ) : (
          <ul className={`${n.list} ${n.inboxList}`} aria-label={unreadOnly ? "Unread notifications" : "Notifications"}>
            {items.map((item) => (
              <NotificationRow key={item.id} item={item} viewer={me} onRead={(i) => one.mutate(i.id)} />
            ))}
          </ul>
        )}
        <LoadMore query={list} />
      </Card>
      <ErrorAlert error={one.error ?? all.error} title="Couldn't mark as read" />
    </Stack>
  );
}
