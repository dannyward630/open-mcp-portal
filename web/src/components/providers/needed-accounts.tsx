/*
 * The accounts a gateway's capabilities need, for the person (My gateways ›
 * gateway): one row per provider with the scopes its capabilities need
 * together, whether the person's connection covers them all now, and
 * Connect, which asks the provider for those scopes and comes back here.
 * Without connecting first, the first call that needs one answers with a
 * link instead; connecting here saves that step. A capability whose server
 * is in oauth mode needs a sign-in to that server itself ("Sign in to …").
 */
import { Link } from "@tanstack/react-router";
import { StatusBadge } from "@/components/ui/badge/badge";
import { Card } from "@/components/ui/card/card";
import { Table, Td, Tr } from "@/components/ui/table/table";
import { TextLink } from "@/components/ui/text-link/text-link";
import s from "../../pages/shared.module.css";
import type { GatewayCapability } from "../gateways/labels";
import { scopesText } from "./labels";
import { ConnectButton } from "./requires";

export type NeededAccount = { providerId: string; providerName: string; scopes: string[]; connected: boolean; uses: number; server: boolean };

/** The providers the capabilities need, by name: their scopes together, and connected only when every capability's are covered. */
export function neededAccounts(caps: Pick<GatewayCapability, "requires">[]): NeededAccount[] {
  const out = new Map<string, NeededAccount>();
  for (const { requires: r } of caps) {
    if (!r) continue;
    const a = out.get(r.providerId) ?? { providerId: r.providerId, providerName: r.providerName, scopes: [], connected: true, uses: 0, server: r.providerKind === "mcp_server" };
    a.scopes = [...new Set([...a.scopes, ...r.scopes])];
    a.connected = a.connected && r.connected === true;
    a.uses += 1;
    out.set(r.providerId, a);
  }
  return [...out.values()].sort((a, b) => a.providerName.localeCompare(b.providerName));
}

export function NeededAccountsCard({ caps, returnTo }: { caps: Pick<GatewayCapability, "requires">[]; returnTo: string }) {
  const accounts = neededAccounts(caps);
  if (accounts.length === 0) return null;
  return (
    <Card
      title="Accounts it needs"
      description={
        <>
          Some capabilities act as you on another service, or need you to sign in to their server. Connect each account once; the portal keeps the token and sends it only to the server that needs it. See
          them all in <TextLink render={<Link to="/settings/connections" />}>Account › My connections</TextLink>.
        </>
      }
      flush
    >
      <Table caption="Accounts this gateway needs" columns={["Provider", "Scopes", "Status", { label: "", stickyEnd: true }]} stack>
        {accounts.map((a) => (
          <Tr key={a.providerId}>
            <Td>
              <span className={s.primary}>{a.providerName}</span>
              <span className={s.secondary}>
                {a.server ? "MCP server sign-in · " : ""}
                {a.uses === 1 ? "1 capability" : `${a.uses} capabilities`}
              </span>
            </Td>
            <Td>
              <span className={s.mono}>{scopesText(a.scopes, "Default access")}</span>
            </Td>
            <Td>
              {a.connected ? (
                <StatusBadge tone="success">Connected</StatusBadge>
              ) : (
                <StatusBadge tone="warning">Not connected</StatusBadge>
              )}
            </Td>
            <Td stickyEnd>
              {!a.connected && (
                <ConnectButton providerId={a.providerId} providerName={a.providerName} scopes={a.scopes} returnTo={returnTo} label={a.server ? `Sign in to ${a.providerName}` : undefined} />
              )}
            </Td>
          </Tr>
        ))}
      </Table>
    </Card>
  );
}
