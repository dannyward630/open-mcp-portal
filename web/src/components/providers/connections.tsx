/*
 * Connected accounts (decision 29): the provider accounts a person connected
 * so tools can act as them, one per provider, with the scopes granted, the
 * status (active, or needs reconnecting after a failed refresh or a new
 * client ID), when it was connected and last used, and Disconnect (asked
 * first; the tokens are erased at once and revoked at the provider where it
 * can). Account › My connections shows the person's own, with Connect again;
 * Admin › Users › person shows anyone's, where platform admins disconnect
 * and auditors read. Tokens are never shown anywhere.
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { KeyRound, RefreshCw, Unplug } from "lucide-react";
import { type ReactNode, useState } from "react";
import { api, errorMessage, unwrap } from "../../api/client";
import { myConnectionsKey } from "../../api/queries";
import { formatDate, toDate } from "../../lib/bitop-format";
import { ConfirmMutationDialog } from "../confirm-dialog";
import { ListPage, RelativeTime, timeColumn } from "../templates/list-page";
import { StatusBadge } from "@/components/ui/badge/badge";
import { CellText, type DataTableColumn } from "@/components/ui/data-table/data-table";
import { toast } from "@/components/ui/toast/toast";
import s from "../../pages/shared.module.css";
import { connectionStatusLabels, connectionStatusTone, type ProviderConnection, scopesText } from "./labels";
import { useStartConnection } from "./requires";

/** Whose accounts: the signed-in person's, or a person's on their admin page. */
export type ConnectionsOwner = { self: true } | { self: false; userId: string; name: string };

export const connectionsKey = (owner: ConnectionsOwner) => (owner.self ? myConnectionsKey : ["admin", "user", owner.userId, "connections"]);

/** Where connecting from Account › My connections comes back to: the page, saying which provider was connected. */
export const connectedReturnTo = (providerSlug: string) => `/settings/connections?connected=${encodeURIComponent(providerSlug)}`;

export function useConnections(owner: ConnectionsOwner) {
  return useQuery({
    queryKey: connectionsKey(owner),
    queryFn: async () =>
      owner.self
        ? unwrap(await api.GET("/v1/me/connections"))
        : unwrap(await api.GET("/v1/admin/users/{userId}/connections", { params: { path: { userId: owner.userId } } })),
  });
}

function useDisconnect(owner: ConnectionsOwner, onDone: () => void) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (c: ProviderConnection) =>
      owner.self
        ? unwrap(await api.DELETE("/v1/me/connections/{connectionId}", { params: { path: { connectionId: c.id } } }))
        : unwrap(await api.DELETE("/v1/admin/users/{userId}/connections/{connectionId}", { params: { path: { userId: owner.userId, connectionId: c.id } } })),
    onSuccess: (_, c) => {
      onDone();
      toast.success(`${c.provider.name} was disconnected`, owner.self ? "Its tokens were erased." : undefined);
    },
    onSettled: () => {
      void qc.invalidateQueries({ queryKey: connectionsKey(owner) });
      // What My gateways says about being connected changes too.
      if (owner.self) void qc.invalidateQueries({ queryKey: ["my", "gateways"] });
    },
  });
}

/** "in 40 minutes" (the exact time as its tooltip); "No expiry" when the provider gave none. */
function TokenExpiry({ c }: { c: ProviderConnection }) {
  const date = toDate(c.expiresAt);
  if (c.status !== "active") return <span className={s.muted}>—</span>;
  if (!date) return <span className={s.muted}>No expiry</span>;
  return (
    <span className={s.nowrap} title={formatDate(date, { style: "long" })}>
      <RelativeTime value={c.expiresAt!} />
      {c.refreshable ? <span className={s.secondary}>Renewed automatically</span> : null}
    </span>
  );
}

function columns(): DataTableColumn<ProviderConnection>[] {
  return [
    {
      id: "provider",
      header: "Provider",
      accessor: (c) => c.provider.name,
      rowHeader: true,
      hideable: false,
      sortable: true,
      cell: (c) => <CellText wrap="anywhere" primary={c.provider.name} secondary={c.accountLabel ? `Account: ${c.accountLabel}` : undefined} />,
    },
    // On a phone the provider (with its account) and status stay in view; the Columns menu still offers the rest.
    { id: "scopes", header: "Scopes", accessor: (c) => scopesText(c.scopes), defaultHiddenNarrow: true, cell: (c) => <span className={s.mono}>{scopesText(c.scopes, "—")}</span> },
    {
      id: "status",
      header: "Status",
      accessor: (c) => connectionStatusLabels[c.status],
      sortable: true,
      cell: (c) => <StatusBadge tone={connectionStatusTone[c.status]}>{connectionStatusLabels[c.status]}</StatusBadge>,
    },
    { ...timeColumn<ProviderConnection>("connectedAt", "Connected", (c) => c.connectedAt), defaultHiddenNarrow: true },
    {
      ...timeColumn<ProviderConnection>("lastUsedAt", "Last used", (c) => c.lastUsedAt),
      defaultHiddenNarrow: true,
      cell: (c) => (c.lastUsedAt ? <span className={s.nowrap}><RelativeTime value={c.lastUsedAt} /></span> : <span className={s.muted}>Not used yet</span>),
    },
    { id: "expiresAt", header: "Token expires", accessor: (c) => (c.expiresAt ? new Date(c.expiresAt) : null), defaultHidden: true, cell: (c) => <TokenExpiry c={c} /> },
  ];
}

type Props = {
  owner: ConnectionsOwner;
  canDisconnect: boolean;
  /** A page of its own (Account › My connections): its title, description and primary action. Without a title, only the list. */
  title?: string;
  description?: string;
  primaryAction?: ReactNode;
  notices?: ReactNode;
  /** The empty list's action (Connect an account). */
  emptyAction?: ReactNode;
};

export function ProviderConnections({ owner, canDisconnect, title, description, primaryAction, notices, emptyAction }: Props) {
  const connections = useConnections(owner);
  const [target, setTarget] = useState<ProviderConnection | null>(null);
  const disconnect = useDisconnect(owner, () => setTarget(null));
  const start = useStartConnection();
  const reconnect = (c: ProviderConnection) =>
    start.mutate(
      { providerId: c.provider.id, ...(c.scopes.length ? { scopes: c.scopes } : {}), returnTo: connectedReturnTo(c.provider.slug) },
      { onError: (err) => toast.error(`Couldn't start connecting ${c.provider.name}`, errorMessage(err)) },
    );
  const who = owner.self ? "you" : owner.name;
  return (
    <>
      <ListPage<ProviderConnection>
        id={owner.self ? "my-connections" : "admin-user-connections"}
        title={title}
        description={description}
        primaryAction={primaryAction}
        notices={notices}
        caption={owner.self ? "Your connected accounts" : `Accounts ${owner.name} connected`}
        columns={columns()}
        data={connections.data ?? []}
        getRowId={(c) => c.id}
        rowLabel={(c) => c.provider.name}
        loading={connections.isLoading}
        error={connections.error}
        onRetry={() => void connections.refetch()}
        rowActions={(c) => [
          {
            label: c.status === "needs_reconsent" ? "Reconnect" : "Connect again",
            icon: <RefreshCw aria-hidden />,
            hidden: !owner.self,
            disabled: start.isPending || start.isSuccess,
            onSelect: () => reconnect(c),
          },
          { label: "Disconnect…", icon: <Unplug aria-hidden />, danger: true, hidden: !canDisconnect, onSelect: () => setTarget(c) },
        ]}
        empty={{
          icon: <KeyRound />,
          title: "No connected accounts.",
          description: owner.self
            ? "Some tools act as you on another service, such as GitHub or Google. The first time you use one, your AI client shows a link to connect your account here, once. You can also connect one ahead of time."
            : `Accounts ${who} connects so tools can act as them, such as GitHub or Google, are listed here, one per provider.`,
          action: emptyAction,
        }}
        tableProps={{ columnsMenuMin: 5 }}
      />
      <ConfirmMutationDialog
        target={target}
        onClose={() => setTarget(null)}
        mutation={disconnect}
        onConfirm={(c) => disconnect.mutate(c)}
        title={`Disconnect ${target?.provider.name ?? "this account"}${owner.self ? "" : ` for ${owner.name}`}?`}
        description={`Its tokens are erased now${target ? " and revoked at the provider where it allows it" : ""}. Tools that need ${owner.self ? "your" : `${owner.name}'s`} ${
          target?.provider.name ?? ""
        } account ask ${owner.self ? "you" : "them"} to connect it again the next time.`}
        confirmLabel="Disconnect"
      />
    </>
  );
}
