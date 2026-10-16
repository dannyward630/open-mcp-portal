import { Outlet, createRootRoute, createRoute, createRouter, lazyRouteComponent, redirect } from "@tanstack/react-router";
import { type ReactNode } from "react";
import { AppLayout } from "./components/layout/shell";
import { NotFoundState } from "./components/not-found";
import { adminTeamTabs, serverTabs, tabSearch, teamSettingsTabs } from "./lib/tabs";
import { InstanceSync, SignInPage, useCurrentUser, useMe } from "./session";
import { useSessionUnavailable } from "./components/layout/session-unavailable";
import { ColorModeSync } from "./components/layout/theme";
import { Loading } from "@/components/ui/spinner/spinner";
import { Toaster } from "@/components/ui/toast/toast";
import styles from "./router.module.css";

/*
 * Pages load on demand, one chunk per page module (a feature folder's
 * routes.ts groups its pages into one); the shell (router, session, layout
 * and UI library) stays in the main bundle.
 *
 * Areas: the team's MCP servers under teams/$team, the Catalog at the top
 * level, MCP servers and Publishing under admin. Later phases add theirs
 * (gateways under teams/$team, Connect at the top level) here and in
 * components/layout/nav.tsx.
 */
const pages = {
  home: () => import("./pages/home"),
  account: () => import("./pages/account/routes"),
  people: () => import("./pages/admin/people/routes"),
  adminAudit: () => import("./pages/admin/audit/page"),
  teamLayout: () => import("./pages/team/layout"),
  teamSettings: () => import("./pages/team/settings/page"),
  teamServers: () => import("./pages/team/servers/routes"),
  catalog: () => import("./pages/catalog/routes"),
  adminServers: () => import("./pages/admin/servers/routes"),
};
const lazy = lazyRouteComponent;
type Page = ReturnType<typeof lazyRouteComponent>;

/**
 * Shows the sign-in page until there is a session. While the session can't
 * load (a rate limit, a server error), a page says so and retries; once it
 * has loaded, a failed background refresh keeps the app as it is.
 */
function SessionGate({ children }: { children: ReactNode }) {
  const me = useMe();
  const unavailable = useSessionUnavailable(me);
  if (me.data === undefined) return unavailable ?? <Loading className={styles.fullPage} label="Loading…" />;
  if (!me.data) return <SignInPage />;
  return <>{children}</>;
}

/**
 * The admin portal is only for platform admins and auditors. Others get the
 * no-access page in the workspace shell, and no admin page (or admin API
 * request) is loaded.
 */
function AdminGate() {
  const { capabilities } = useCurrentUser();
  if (!capabilities.platformAdmin && !capabilities.platformAuditor) return <NotFoundState what="forbidden" />;
  return <Outlet />;
}

function Root() {
  // Toasts sit over the sidebar's footer, so they never cover page content.
  return (
    <>
      <InstanceSync />
      <ColorModeSync />
      <Outlet />
      <Toaster position="bottom-left" />
    </>
  );
}

// The session gate wraps the app (not the root), so routes outside it stay possible. Route ids are "/app/...".
const rootRoute = createRootRoute({
  component: Root,
  notFoundComponent: () => (
    <SessionGate>
      <AppLayout>
        <NotFoundState />
      </AppLayout>
    </SessionGate>
  ),
});
const appRoute = createRoute({
  getParentRoute: () => rootRoute,
  id: "app",
  component: () => (
    <SessionGate>
      <AppLayout />
    </SessionGate>
  ),
});

const homeRoute = createRoute({ getParentRoute: () => appRoute, path: "/", component: lazy(pages.home, "HomePage") });
const profileRoute = createRoute({ getParentRoute: () => appRoute, path: "settings/profile", component: lazy(pages.account, "ProfilePage") });

/* ---------------- the catalog (everyone signed in) ---------------- */

/** Published servers; ?q= passes through. */
const catalogRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "catalog",
  validateSearch: (s: Record<string, unknown>): Record<string, unknown> => s,
  component: lazy(pages.catalog, "CatalogPage"),
});
const catalogServerRoute = createRoute({ getParentRoute: () => appRoute, path: "catalog/$serverId", component: lazy(pages.catalog, "CatalogServerPage") });

/* ---------------- a team (workspace) ---------------- */

const teamRoute = createRoute({ getParentRoute: () => appRoute, path: "teams/$team", component: lazy(pages.teamLayout, "TeamLayout") });
const teamIndexRoute = createRoute({ getParentRoute: () => teamRoute, path: "/", component: lazy(pages.teamLayout, "TeamOverviewPage") });
/** Members · Audit log · General; ?record= (an audit entry) and the log's filters pass through. */
const teamSettingsRoute = createRoute({
  getParentRoute: () => teamRoute,
  path: "settings",
  validateSearch: tabSearch(teamSettingsTabs, { passthrough: true }),
  component: lazy(pages.teamSettings, "TeamSettingsPage"),
});
/** The team's MCP servers; ?q= and the add form (?form=new) pass through. */
const teamServersRoute = createRoute({
  getParentRoute: () => teamRoute,
  path: "servers",
  validateSearch: (s: Record<string, unknown>): Record<string, unknown> => s,
  component: lazy(pages.teamServers, "TeamServersPage"),
});
/** A server: Overview · Capabilities · Health · Settings; the capabilities' filters (?kind=, ?state=) pass through. */
const teamServerRoute = createRoute({
  getParentRoute: () => teamRoute,
  path: "servers/$serverId",
  validateSearch: tabSearch(serverTabs, { passthrough: true }),
  component: lazy(pages.teamServers, "TeamServerPage"),
});

/* ---------------- admin ---------------- */

const adminRoute = createRoute({ getParentRoute: () => appRoute, path: "admin", component: AdminGate });
/** The admin portal opens on its first page. */
const adminIndexRoute = createRoute({
  getParentRoute: () => adminRoute,
  path: "/",
  beforeLoad: () => {
    throw redirect({ to: "/admin/users", replace: true });
  },
});
/** An admin list page whose filters (?q=, ?status=…) and open record (?record=) live in the URL. */
const adminList = <P extends string>(path: P, component: Page) =>
  createRoute({ getParentRoute: () => adminRoute, path, validateSearch: (s: Record<string, unknown>): Record<string, unknown> => s, component });
/** An admin page with tabs; list filters, ?q= and ?record= pass through. */
const adminTabs = <P extends string, T extends string>(path: P, tabs: readonly T[], component: Page) =>
  createRoute({ getParentRoute: () => adminRoute, path, validateSearch: tabSearch(tabs, { passthrough: true }), component });

const appTree = appRoute.addChildren([
  homeRoute,
  profileRoute,
  catalogRoute,
  catalogServerRoute,
  teamRoute.addChildren([teamIndexRoute, teamSettingsRoute, teamServersRoute, teamServerRoute]),
  adminRoute.addChildren([
    adminIndexRoute,
    adminList("users", lazy(pages.people, "AdminUsersPage")),
    adminList("users/$userId", lazy(pages.people, "AdminUserPage")),
    adminList("teams", lazy(pages.people, "AdminTeamsPage")),
    adminTabs("teams/$team", adminTeamTabs, lazy(pages.people, "AdminTeamPage")),
    adminList("servers", lazy(pages.adminServers, "AdminServersPage")),
    adminTabs("servers/$serverId", serverTabs, lazy(pages.adminServers, "AdminServerPage")),
    adminList("publishing", lazy(pages.adminServers, "AdminPublishingPage")),
    adminList("audit", lazy(pages.adminAudit, "AdminAuditPage")),
  ]),
]);

export const routeTree = rootRoute.addChildren([appTree]);

export const router = createRouter({
  routeTree,
  defaultPreload: "intent",
  // Shown while a page's chunk loads (after a short delay, so fast loads don't flash).
  defaultPendingComponent: () => <Loading className={styles.pending} label="Loading…" />,
});

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}
