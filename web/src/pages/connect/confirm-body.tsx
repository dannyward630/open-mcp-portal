/*
 * The connect page's confirmation: the tool and gateway that asked, the
 * provider and the account connected now, each scope it will ask for
 * (already granted, or new), where the token goes, and Continue or Cancel.
 */
import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { type ConnectRequest, presetLabels } from "../../components/providers/labels";
import { Alert, ErrorAlert } from "@/components/ui/alert/alert";
import { Badge } from "@/components/ui/badge/badge";
import { Button } from "@/components/ui/button/button";
import { Stack } from "@/components/ui/layout/layout";
import { TextLink } from "@/components/ui/text-link/text-link";
import { Time } from "@/components/ui/time/time";
import { VisuallyHidden } from "@/components/ui/visually-hidden/visually-hidden";
import styles from "../oauth/consent.module.css";
import c from "./connect.module.css";

const kindWords = { tool: "tool", prompt: "prompt", resource: "resource", resource_template: "resource template" } as const;

/** Why the tool asked, in a sentence. */
function reasonText(r: ConnectRequest, what: string) {
  const p = r.provider.name;
  switch (r.reason) {
    case "missing_scopes":
      return `${what} needs more access to your ${p} account than you allowed before.`;
    case "needs_reconsent":
      return `Your ${p} connection has to be renewed: it stopped working or the provider's app changed. ${what} needs it.`;
    default:
      return `${what} needs your ${p} account to act as you.`;
  }
}

type Props = {
  request: ConnectRequest;
  titleId: string;
  email: string;
  start: { run: () => void; pending: boolean; error: unknown };
  leaving: string | null;
};

export function ConfirmBody({ request: r, titleId, email, start, leaving }: Props) {
  const [cancelled, setCancelled] = useState(false);
  const p = r.provider.name;
  const what = `The ${kindWords[r.capability.kind]} ${r.capability.exposedName}`;
  const granted = new Set(r.grantedScopes);
  const busy = start.pending || leaving !== null;
  if (cancelled) {
    return (
      <Stack gap={4}>
        <h1 id={titleId} className={styles.title}>
          Nothing was connected
        </h1>
        <p className={styles.muted}>
          You cancelled, so {p} wasn&apos;t contacted. You can close this page. {what} keeps asking until you connect your account.
        </p>
      </Stack>
    );
  }
  return (
    <Stack gap={5}>
      <div>
        <h1 id={titleId} className={styles.title}>
          Connect your {p} account
        </h1>
        <p className={styles.muted}>{reasonText(r, what)}</p>
      </div>
      <section aria-labelledby={`${titleId}-who`}>
        <h2 id={`${titleId}-who`} className={styles.heading}>
          The request
        </h2>
        <dl className={styles.facts}>
          <dt>Signed in as</dt>
          <dd>{email}</dd>
          <dt>Asked by</dt>
          <dd>
            <code>{r.capability.exposedName}</code>
            {r.capability.title && r.capability.title !== r.capability.exposedName ? ` (${r.capability.title})` : ""}
          </dd>
          <dt>Gateway</dt>
          <dd>{r.gateway.name}</dd>
          <dt>Provider</dt>
          <dd>{r.provider.preset && presetLabels[r.provider.preset] !== p ? `${p} (${presetLabels[r.provider.preset]})` : p}</dd>
          <dt>Connected now</dt>
          <dd>{r.accountLabel ?? (r.grantedScopes.length ? "An account (no name given)" : "No account")}</dd>
        </dl>
      </section>
      <section aria-labelledby={`${titleId}-scopes`}>
        <h2 id={`${titleId}-scopes`} className={styles.heading}>
          {p} will ask you to allow
        </h2>
        {r.requestScopes.length === 0 ? (
          <p className={styles.muted}>The provider&apos;s default access.</p>
        ) : (
          <ul className={c.scopes} aria-labelledby={`${titleId}-scopes`}>
            {r.requestScopes.map((s) => (
              <li key={s} className={c.scope}>
                <span className={c.scopeName}>{s}</span>
                {granted.has(s) ? (
                  <Badge size="sm" tone="neutral">
                    Already allowed
                  </Badge>
                ) : (
                  <Badge size="sm" tone="info">
                    New
                  </Badge>
                )}
              </li>
            ))}
          </ul>
        )}
      </section>
      <Alert tone="info">
        The gateway keeps the token and sends it only to the server behind {r.capability.exposedName}. Your AI client and its model never see it. Disconnect any time
        from <TextLink render={<Link to="/settings/connections" target="_blank" rel="noreferrer" />}>
          Account › My connections<VisuallyHidden> (opens in a new tab)</VisuallyHidden>
        </TextLink>
        .
      </Alert>
      <p className={styles.muted}>
        After you approve at {p}, you come back here. Then run the tool again. This link expires <Time value={r.expiresAt} />.
      </p>
      {start.error != null && <ErrorAlert error={start.error} title={`Couldn't start connecting ${p}`} />}
      <p role="status" className={styles.muted}>
        {leaving ? `Taking you to ${p}…` : ""}
      </p>
      <div className={styles.actions}>
        <Button variant="secondary" disabled={busy} onClick={() => setCancelled(true)}>
          Cancel
        </Button>
        <Button loading={start.pending} disabled={busy} onClick={start.run}>
          Continue to {p}
        </Button>
      </div>
    </Stack>
  );
}
