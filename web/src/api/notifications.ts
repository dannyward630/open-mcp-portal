/*
 * The person's notifications: the bell's unread count, the latest items, the inbox's pages and the preferences.
 * They live here, not in the page modules, so the shell's bell doesn't pull lazily loaded pages into the main bundle.
 */
import { queryOptions } from "@tanstack/react-query";
import { api, unwrap } from "./client";

/** The query key prefix of everything the person's inbox shows (marking read invalidates it all). */
export const notificationsKey = ["my", "notifications"];

/** How often the bell asks for the unread count while the page is visible (and again when the window gets focus). */
export const unreadPollMs = 60_000;

/** The unread count, polled lightly: every minute while visible, and when the window regains focus. */
export const unreadCountQuery = () =>
  queryOptions({
    queryKey: [...notificationsKey, "unread-count"],
    queryFn: async () => unwrap(await api.GET("/v1/me/notifications/unread-count")),
    select: (c) => c.count,
    refetchInterval: unreadPollMs,
    refetchIntervalInBackground: false,
    refetchOnWindowFocus: true,
  });

/** How many items the bell's popover lists. */
export const popoverCount = 8;

/** The latest notifications (the bell's popover). */
export const latestNotificationsQuery = (enabled: boolean) =>
  queryOptions({
    queryKey: [...notificationsKey, "latest"],
    queryFn: async () => unwrap(await api.GET("/v1/me/notifications", { params: { query: { limit: popoverCount } } })),
    enabled,
  });

/** The inbox's key for the All / Unread filter. */
export const inboxKey = (unread: boolean) => [...notificationsKey, "inbox", unread ? "unread" : "all"];

/** The notification preferences of the person (events they can receive x In-app / Email). */
export const preferencesQuery = () =>
  queryOptions({
    queryKey: ["my", "notification-preferences"],
    queryFn: async () => unwrap(await api.GET("/v1/me/notification-preferences")),
  });
