/*
 * The setup sign-in of a server in oauth mode (decision 42): the gateway
 * reads the server's capabilities, and runs manual tests, with one person's
 * own sign-in. "Authorize to read its tools" (POST …/oauth/authorize-setup,
 * editors and above, browser session) sends the browser to the
 * authorization server; it comes back to the record with ?setup=connected,
 * where the capabilities were read as that person. Any editor can sign in
 * again as themselves when that sign-in lapses.
 */
import { useMutation } from "@tanstack/react-query";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { KeyRound } from "lucide-react";
import { api, unwrap } from "../../api/client";
import { redirectTo, safeAuthorizeUrl, scopesText } from "../providers/labels";
import { Alert, ErrorAlert } from "@/components/ui/alert/alert";
import { Button, type ButtonProps } from "@/components/ui/button/button";
import { describeAuthError } from "./auth-errors";
import { type McpServer, urlHost } from "./labels";
import type { ServerAbilities, ServerScope } from "./scope";
import { authModeOf } from "./upstream-auth";

/** Starts the setup sign-in and leaves for the authorization server; refusals throw (shown by the caller). */
export function useAuthorizeSetup(server: McpServer) {
  return useMutation({
    mutationFn: async () => {
      const out = unwrap(await api.POST("/v1/teams/{team}/mcp-servers/{serverId}/oauth/authorize-setup", { params: { path: { team: server.teamSlug, serverId: server.id } }, body: {} }));
      const target = safeAuthorizeUrl(out.authorizeUrl);
      if (!target) throw new Error("The gateway answered with an address your browser can't go to, so you weren't sent anywhere.");
      return target;
    },
    onSuccess: (target) => redirectTo.go(target),
  });
}

/** Whether this viewer can sign in for the server: an editor or above on the team's page. */
export const canAuthorize = (scope: ServerScope, abilities: ServerAbilities) => scope.kind === "team" && abilities.canEdit;

type ButtonOwnProps = { server: McpServer; label: string; variant?: ButtonProps["variant"]; size?: ButtonProps["size"] };

/** "Authorize to read its tools" / "Re-authorize as me", with its refusal under it. */
export function AuthorizeButton({ server, label, variant = "primary", size = "sm" }: ButtonOwnProps) {
  const start = useAuthorizeSetup(server);
  return (
    <>
      <Button size={size} variant={variant} loading={start.isPending} disabled={start.isSuccess} onClick={() => start.mutate()}>
        <KeyRound aria-hidden /> {label}
      </Button>
      {start.error != null && <ErrorAlert error={start.error} title={describeAuthError(start.error)?.title ?? "Couldn't start signing in"} describe={describeAuthError} />}
    </>
  );
}

type Props = { server: McpServer; scope: ServerScope; abilities: ServerAbilities; justCreated?: boolean };

/** On top of an oauth server's Overview while nobody's sign-in can read its capabilities. */
export function SetupNotice({ server, scope, abilities, justCreated }: Props) {
  const o = server.oauth;
  if (authModeOf(server) !== "oauth" || !o || o.setupConnection.status === "active") return null;
  const lapsed = o.setupConnection.status === "needs_reconsent";
  const title = justCreated
    ? `${server.name} was added. Authorize to read its tools`
    : lapsed
      ? `${o.setupConnection.personName ? `${o.setupConnection.personName}'s` : "The"} sign-in that reads its capabilities has lapsed`
      : "Nobody has signed in to read its tools yet";
  const can = canAuthorize(scope, abilities);
  return (
    <Alert tone={justCreated ? "info" : "warning"} title={title} actions={can ? <AuthorizeButton server={server} label="Authorize to read its tools" /> : undefined}>
      The gateway reads {server.name}&apos;s tools, resources and prompts with one person&apos;s own sign-in.{" "}
      {can
        ? `You'll sign in at ${urlHost(o.authorizationServer)} and allow ${scopesText(o.scopes, "its default access")}, then come back here, and the capabilities are read as you.`
        : "An editor, admin or owner of the team signs in to read them."}
    </Alert>
  );
}

/** After coming back from the setup sign-in (?setup=connected): as whom the capabilities were read, or why they couldn't be. */
export function SetupConnectedNotice({ server }: { server: McpServer }) {
  const search: Record<string, unknown> = useSearch({ strict: false });
  const navigate = useNavigate();
  const c = server.oauth?.setupConnection;
  if (search.setup !== "connected" || c?.status !== "active") return null;
  const dismiss = () => {
    const { setup: _setup, ...rest } = search;
    void navigate({ to: ".", search: rest as never, replace: true });
  };
  if (server.lastRefreshError) {
    return (
      <Alert tone="warning" title="Signed in, but the capabilities couldn't be read" onDismiss={dismiss}>
        {server.lastRefreshError} Press Refresh capabilities to try again.
      </Alert>
    );
  }
  return (
    <Alert tone="success" title={`Capabilities read as ${c.personName ?? "you"}`} onDismiss={dismiss}>
      The gateway read {server.name}&apos;s capabilities with this sign-in, and uses it for tests and refreshes. Everyone else signs in as themselves the first time
      they call it.
    </Alert>
  );
}
