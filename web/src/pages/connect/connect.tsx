/*
 * The connect page (/connect/{provider}?request=R, decision 28): a tool
 * needed the person's account with a provider, so the portal answered the
 * call with this link (JSON-RPC -32042). The page reads the request
 * (GET /v1/connect/requests/R: only the person it was made for gets it),
 * shows who is signed in, the tool and gateway that asked, the provider, the
 * account connected now and each scope (already granted, or new), and only
 * then, on Continue, starts connecting (POST /v1/me/connections/start with
 * the app's CSRF token) and sends the browser to the provider. Cancel leaves
 * everything as it was. A link is for one server (F-3): the page names it with
 * its team, and when the account already has the scopes (reason
 * server_not_allowed) the button allows the server with POST
 * /v1/connect/requests/R/confirm and goes to the result page, without
 * leaving for the provider. A link for a server that moved (410) is explained
 * like an expired one.
 *
 * Another person's or an unknown link (404), and an expired or used one
 * (410), are explained here and never sent anywhere. No app shell; signed
 * out, the session gate shows the sign-in page, which returns here with the
 * whole query.
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate, useParams, useSearch } from "@tanstack/react-router";
import { useEffect, useId, useState } from "react";
import { ApiError, api, unwrap } from "../../api/client";
import { type ConnectRequest, redirectTo, safeAuthorizeUrl } from "../../components/providers/labels";
import { useCurrentUser, useInstance } from "../../session";
import { ErrorAlert } from "@/components/ui/alert/alert";
import { Card, CardBody } from "@/components/ui/card/card";
import { Stack } from "@/components/ui/layout/layout";
import { Loading } from "@/components/ui/spinner/spinner";
import styles from "../oauth/consent.module.css";
import { ConfirmBody } from "./confirm-body";

/** Why a link can't be used, in words: someone else's or unknown (404), expired or used (410), or not this provider's. */
export function linkProblem(err: unknown): { title: string; text: string } {
  if (err instanceof ApiError && err.status === 404)
    return {
      title: "This link was made for someone else",
      text: "A connect link works only for the person whose tool asked for it, signed in as themselves. If you're signed in with another account, sign out and sign in as yourself, then open the link again. Otherwise, run the tool again to get a new link.",
    };
  if (err instanceof ApiError && err.status === 410)
    return { title: "This link expired", text: "Links work for 10 minutes and only once. Run the tool again in your AI client to get a new link." };
  if (err === "mismatch") return { title: "This link isn't valid", text: "It names a different provider than the request it carries. Run the tool again to get a new link." };
  if (err === "missing") return { title: "This link is incomplete", text: "It's missing the request it's for. Copy the whole link from your AI client, or run the tool again." };
  return { title: "Can't check this link", text: "The link couldn't be checked right now. Try again in a moment." };
}

export function ConnectPage() {
  const instance = useInstance();
  const me = useCurrentUser();
  const { provider } = useParams({ from: "/connect/$provider" });
  const search = useSearch({ from: "/connect/$provider" });
  const requestId = typeof search.request === "string" ? search.request : "";
  const titleId = useId();
  const navigate = useNavigate();
  const qc = useQueryClient();
  const view = useQuery({
    queryKey: ["connect", "request", requestId],
    queryFn: async () => unwrap(await api.GET("/v1/connect/requests/{requestId}", { params: { path: { requestId } } })),
    enabled: Boolean(requestId),
    retry: false,
    staleTime: Infinity,
  });
  const [leaving, setLeaving] = useState<string | null>(null);
  const start = useMutation({
    mutationFn: async (r: ConnectRequest) => {
      const out = unwrap(await api.POST("/v1/me/connections/start", { body: { providerId: r.provider.id, requestId: r.id } }));
      const target = safeAuthorizeUrl(out.authorizeUrl);
      if (!target) throw new Error("The portal answered with an address your browser can't go to, so you weren't sent anywhere.");
      return target;
    },
    onSuccess: (target) => {
      setLeaving(target);
      redirectTo.go(target);
    },
  });
  const allow = useMutation({
    mutationFn: async (r: ConnectRequest) => unwrap(await api.POST("/v1/connect/requests/{requestId}/confirm", { params: { path: { requestId: r.id } } })),
    onSuccess: (out) => navigate({ to: "/connect/done", search: { provider: out.provider.slug, result: "connected" } }),
    // The account changed since the page loaded (no longer connected with the scopes): look again, the page then offers the provider.
    onError: (err) => {
      if (err instanceof ApiError && err.status === 409) void qc.invalidateQueries({ queryKey: ["connect", "request", requestId] });
    },
  });
  // A request for another provider than the link names is refused: the page never shows one provider and starts another.
  const problem: unknown = !requestId ? "missing" : view.data && view.data.provider.slug !== provider ? "mismatch" : view.error != null ? view.error : null;
  // Continue can find the link expired (or used in another tab): say so the same way.
  const expired = (e: unknown) => (e instanceof ApiError && (e.status === 404 || e.status === 410) ? e : null);
  const startProblem = expired(start.error) ?? expired(allow.error);
  const shown = problem ?? startProblem;
  const providerName = view.data?.provider.name;
  useEffect(() => {
    if (shown != null) document.title = `${linkProblem(shown).title} · ${instance.name}`;
    else if (providerName) document.title = `Connect ${providerName} · ${instance.name}`;
  }, [providerName, shown, instance.name]);

  return (
    <main className={styles.page} aria-labelledby={view.data || shown != null ? titleId : undefined}>
      <div className={styles.column}>
        <p className={styles.instance}>{instance.name}</p>
        <Card>
          <CardBody>
            {view.isLoading && <Loading label="Checking the link…" />}
            {shown != null ? (
              <Problem titleId={titleId} problem={shown} email={me.user.email} />
            ) : (
              view.data && (
                <ConfirmBody
                  request={view.data}
                  titleId={titleId}
                  email={me.user.email}
                  start={{ run: () => start.mutate(view.data!), pending: start.isPending, error: startProblem ? null : start.error }}
                  allow={{ run: () => allow.mutate(view.data!), pending: allow.isPending, error: startProblem ? null : allow.error }}
                  leaving={leaving}
                />
              )
            )}
          </CardBody>
        </Card>
      </div>
    </main>
  );
}

function Problem({ titleId, problem, email }: { titleId: string; problem: unknown; email: string }) {
  const { title, text } = linkProblem(problem);
  const other = !(problem instanceof ApiError && (problem.status === 404 || problem.status === 410)) && typeof problem !== "string";
  return (
    <Stack gap={4}>
      <h1 id={titleId} className={styles.title}>
        {title}
      </h1>
      <p className={styles.muted}>{text}</p>
      {other && <ErrorAlert error={problem} />}
      <p className={styles.muted}>
        Signed in as <strong>{email}</strong>. Nothing was connected, and this page won&apos;t send you anywhere.
      </p>
    </Stack>
  );
}
