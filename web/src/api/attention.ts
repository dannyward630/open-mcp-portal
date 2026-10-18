/* What needs attention: the platform overview (Admin › Overview) and a team's card, refreshed every minute while visible. */
import { queryOptions } from "@tanstack/react-query";
import { api, unwrap } from "./client";

/** How often both ask again while the page is visible (and when the window regains focus). */
export const attentionPollMs = 60_000;

export const adminOverviewQuery = () =>
  queryOptions({
    queryKey: ["admin", "overview"],
    queryFn: async () => unwrap(await api.GET("/v1/admin/overview")),
    refetchInterval: attentionPollMs,
    refetchIntervalInBackground: false,
    refetchOnWindowFocus: true,
  });

export const teamAttentionQuery = (team: string, enabled = true) =>
  queryOptions({
    queryKey: ["team", team, "attention"],
    queryFn: async () => unwrap(await api.GET("/v1/teams/{team}/attention", { params: { path: { team } } })),
    enabled,
    refetchInterval: attentionPollMs,
    refetchIntervalInBackground: false,
    refetchOnWindowFocus: true,
  });
