/*
 * The notification bell in the top bar: the unread count (asked for every minute while the page is visible, and when
 * the window regains focus) and a popover with the latest notifications, "Mark all read" and "View all". Failing to
 * load the count shows nothing (the bell still opens the inbox). A visually hidden status line announces the count
 * only when it changes, never on first load, so a screen reader isn't told on every poll.
 */
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Bell, CheckCheck } from "lucide-react";
import { useState } from "react";
import { latestNotificationsQuery, unreadCountQuery } from "../../api/notifications";
import { useCurrentUser } from "../../session";
import { ErrorAlert } from "@/components/ui/alert/alert";
import { Badge } from "@/components/ui/badge/badge";
import { Button } from "@/components/ui/button/button";
import { Popover } from "@/components/ui/popover/popover";
import { Loading } from "@/components/ui/spinner/spinner";
import { TextLink } from "@/components/ui/text-link/text-link";
import { VisuallyHidden } from "@/components/ui/visually-hidden/visually-hidden";
import n from "./notifications.module.css";
import { NotificationRow } from "./row";
import { useMarkRead } from "./use-mark-read";

const countText = (count: number) => (count === 0 ? "No unread notifications" : `${count} unread ${count === 1 ? "notification" : "notifications"}`);

/** The count's announcement: empty until the count first changes after it was known. */
function useCountAnnouncement(count: number | undefined): string {
  const [seen, setSeen] = useState<{ count?: number; message: string }>({ message: "" });
  // A state update during render: the message is known in the same render as the new count.
  if (count !== undefined && count !== seen.count) setSeen({ count, message: seen.count === undefined ? "" : countText(count) });
  return seen.message;
}

export function NotificationBell() {
  const me = useCurrentUser();
  const [open, setOpen] = useState(false);
  const unread = useQuery(unreadCountQuery());
  const count = unread.data;
  const announcement = useCountAnnouncement(count);
  const label = count ? `Notifications, ${count} unread` : "Notifications";
  return (
    <>
      <Popover
        open={open}
        onOpenChange={setOpen}
        align="end"
        title="Notifications"
        trigger={
          <Button variant="ghost" iconOnly aria-label={label} className={n.bell}>
            <Bell aria-hidden />
            {Boolean(count) && (
              <Badge tone="danger" size="sm" className={n.count} aria-hidden>
                {(count ?? 0) > 99 ? "99+" : count}
              </Badge>
            )}
          </Button>
        }
      >
        {open && <Latest unread={count ?? 0} onClose={() => setOpen(false)} viewer={me} />}
      </Popover>
      <VisuallyHidden role="status" aria-live="polite" aria-atomic>
        {announcement}
      </VisuallyHidden>
    </>
  );
}

function Latest({ unread, onClose, viewer }: { unread: number; onClose: () => void; viewer: ReturnType<typeof useCurrentUser> }) {
  const latest = useQuery(latestNotificationsQuery(true));
  const { one, all } = useMarkRead();
  const items = latest.data?.items ?? [];
  const hasUnread = (latest.data?.unreadCount ?? unread) > 0;
  return (
    <div className={n.panel}>
      <div className={n.panelHead}>
        <span className={n.meta} aria-live="off">
          {latest.data ? countText(latest.data.unreadCount) : ""}
        </span>
        <Button size="sm" variant="secondary" disabled={!hasUnread} loading={all.isPending} onClick={() => all.mutate()}>
          <CheckCheck aria-hidden /> Mark all read
        </Button>
      </div>
      {latest.isLoading ? (
        <Loading label="Loading notifications…" />
      ) : latest.error ? (
        <ErrorAlert error={latest.error} title="Couldn't load notifications" onRetry={() => void latest.refetch()} />
      ) : items.length === 0 ? (
        <p className={n.empty}>You&apos;re all caught up.</p>
      ) : (
        <ul className={n.list} aria-label="Latest notifications">
          {items.map((item) => (
            <NotificationRow key={item.id} item={item} viewer={viewer} onRead={(i) => one.mutate(i.id)} onOpen={onClose} />
          ))}
        </ul>
      )}
      <ErrorAlert error={one.error ?? all.error} title="Couldn't mark as read" />
      <div className={n.panelFoot}>
        <TextLink render={<Link to="/notifications" />} onClick={onClose}>
          View all
        </TextLink>
        <TextLink render={<Link to="/settings/notifications" />} onClick={onClose}>
          Settings
        </TextLink>
      </div>
    </div>
  );
}
