/*
 * Detect sign-in (POST …/mcp-servers/detect-auth, editors and above): the
 * gateway asks the URL how clients sign in, as an MCP client would, and
 * says what it found: the authorization server, the scopes it supports,
 * PKCE, how the portal can get a client (automatically, a platform
 * provider's, or one the team creates) and any problem, with "Use …" when
 * the server names itself with a slightly different URL. Nothing is saved
 * or audited; the server runs the discovery again on save.
 */
import { useMutation } from "@tanstack/react-query";
import { ScanSearch } from "lucide-react";
import { api, unwrap } from "../../api/client";
import { scopesText } from "../providers/labels";
import { ApiErrorAlert } from "../errors";
import { Alert } from "@/components/ui/alert/alert";
import { Badge } from "@/components/ui/badge/badge";
import { Button } from "@/components/ui/button/button";
import { DescriptionList } from "@/components/ui/description-list/description-list";
import { Disclosure } from "@/components/ui/disclosure/disclosure";
import s from "../../pages/shared.module.css";
import { urlHost } from "./labels";
import { serverFieldErrors } from "./server-fields";
import { blockingProblems, type DetectResult, otherHost, registrationLabels, supportedScopes } from "./upstream-auth";
import m from "./servers.module.css";

export function useDetectAuth(team: string) {
  return useMutation({
    mutationFn: async (url: string) => unwrap(await api.POST("/v1/teams/{team}/mcp-servers/detect-auth", { params: { path: { team } }, body: { url } })),
  });
}

type Props = {
  team: string;
  url: string;
  /** What's wrong with the URL as typed: detection waits until it's fixed. */
  urlError?: string;
  disabled?: boolean;
  result?: DetectResult;
  onResult: (r: DetectResult) => void;
  onUseUrl: (url: string) => void;
};

/** The button and what it found. */
export function DetectSignIn({ team, url, urlError, disabled, result, onResult, onUseUrl }: Props) {
  const detect = useDetectAuth(team);
  const refused = serverFieldErrors(detect.error).url;
  const stale = result && result.url !== url.trim() && result.suggestedUrl !== url.trim();
  const run = (target: string) => detect.mutate(target, { onSuccess: onResult });
  return (
    <div className={m.stack}>
      <div className={m.detectRow}>
        <Button
          variant="secondary"
          loading={detect.isPending}
          disabled={disabled || Boolean(urlError)}
          onClick={() => run(url.trim())}
        >
          <ScanSearch aria-hidden /> Detect sign-in
        </Button>
        <span className={s.muted}>{urlError ? "Enter the server's URL first." : "Asks the server how clients sign in. Nothing is saved."}</span>
      </div>
      {refused ? <Alert tone="danger" title="The portal won't connect to this address">{refused}</Alert> : <ApiErrorAlert error={detect.error} title="Couldn't detect the sign-in" />}
      {result && !detect.isPending && (
        <>
          {stale && <Alert tone="info">This is what {result.url} answered. The URL changed since: press Detect sign-in again.</Alert>}
          <DetectFindings
            result={result}
            onUseUrl={(next) => {
              // The suggested URL is the server's own name for itself: use it and ask again with it.
              onUseUrl(next);
              run(next);
            }}
          />
        </>
      )}
    </div>
  );
}

/** What the server said about signing in. */
export function DetectFindings({ result: r, onUseUrl }: { result: DetectResult; onUseUrl: (url: string) => void }) {
  if (r.outcome === "no_auth") {
    return (
      <Alert tone="success" title="Answers without sign-in">
        The server answered without any credentials. Choose None, or a static header if it needs an API key anyway.
      </Alert>
    );
  }
  if (r.outcome === "other_auth") {
    return (
      <Alert tone="info" title="Needs credentials, but doesn't offer OAuth sign-in">
        It answered{r.httpStatus ? ` HTTP ${r.httpStatus}` : ""} without saying how to sign in. Use a static header, such as an API key.
      </Alert>
    );
  }
  if (r.outcome === "unreachable") {
    return (
      <Alert tone="warning" title="Couldn't reach the server">
        {r.httpStatus ? `It answered HTTP ${r.httpStatus}. ` : ""}Check the URL. You can still save the server and choose how to authenticate.
      </Alert>
    );
  }
  return <OAuthFindings result={r} onUseUrl={onUseUrl} />;
}

function OAuthFindings({ result: r, onUseUrl }: { result: DetectResult; onUseUrl: (url: string) => void }) {
  const as = r.authorizationServer;
  const reg = r.registration;
  const scopes = supportedScopes(r);
  const away = as ? otherHost(as.issuer, r.url) : undefined;
  const blocking = blockingProblems(r);
  const automatic = reg?.options.filter((o) => o === "cimd" || o === "dcr") ?? [];
  return (
    <section className={m.stack} aria-label="What the server says about signing in">
      {r.suggestedUrl && (
        <Alert
          tone="warning"
          title={`The server calls itself ${r.suggestedUrl}`}
          actions={
            <Button size="sm" variant="secondary" onClick={() => onUseUrl(r.suggestedUrl!)}>
              Use {r.suggestedUrl}
            </Button>
          }
        >
          Its tokens are issued for that exact address, so register the server with it.
        </Alert>
      )}
      {r.problems.map((p) => (
        <Alert key={p.code} tone={p.blocking ? "danger" : "info"} title={p.blocking ? "Can't use OAuth sign-in" : undefined}>
          {p.message}
        </Alert>
      ))}
      <DescriptionList
        dividers
        items={[
          { label: "Sign-in", value: blocking.length ? "OAuth, with a problem (see above)" : "OAuth: each person signs in" },
          ...(as
            ? [
                {
                  label: "Authorization server",
                  value: (
                    <span className={s.badges}>
                      <code className={m.url}>{as.issuer}</code>
                      {away && <Badge size="sm" tone="warning">{`Another host: ${away}`}</Badge>}
                    </span>
                  ),
                },
                { label: "PKCE", value: as.codeChallengeMethodsSupported.includes("S256") ? "S256 supported" : "Not supported" },
              ]
            : []),
          { label: "Scopes supported", value: <span className={s.mono}>{scopesText(scopes, "None listed")}</span> },
          ...(reg
            ? [
                {
                  label: "Registration",
                  value: (
                    <span className={m.stack}>
                      {automatic.length ? (
                        <span>Automatic: the portal registers itself ({automatic.map((o) => registrationLabels[o].replace(/^Automatic \((.*)\)$/, "$1")).join(" or ")}).</span>
                      ) : (
                        <span>
                          No automatic registration: create an OAuth app at {urlHost(as?.issuer ?? r.url)} with the callback <code className={m.url}>{reg.redirectUri}</code>, then
                          enter its client ID and secret.
                        </span>
                      )}
                      {reg.matchingProviders.length > 0 && <span>Platform providers with the same endpoints: {reg.matchingProviders.map((p) => p.name).join(", ")}.</span>}
                    </span>
                  ),
                },
              ]
            : []),
        ]}
      />
      {r.steps.length > 0 && (
        <Disclosure title="Requests made" summary={`${r.steps.length}`}>
          <ul className={m.steps}>
            {r.steps.map((step, i) => (
              <li key={`${i}-${step.url}`}>
                <code className={m.url}>{step.url}</code> <span className={s.muted}>{`${step.result}${step.httpStatus ? ` (HTTP ${step.httpStatus})` : ""} · ${step.latencyMs} ms`}</span>
              </li>
            ))}
          </ul>
        </Disclosure>
      )}
    </section>
  );
}
