/*
 * Automation tokens of a team (Team settings › Automation tokens) or of the platform (Admin › Automation tokens): the
 * same panel for both. Create (a dialog; the secret once), Revoke (asks first, takes effect on the token's next
 * request). At most 20 active tokens per scope. Tokens are made and revoked in a browser session only, never with a
 * token, and they work on the management API (/v1) only, never on /mcp. Readers (platform staff on a team, auditors)
 * see the list; an archived team's tokens can be revoked but not created.
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Plus } from "lucide-react";
import { useState } from "react";
import { friendly } from "../../lib/error-wording";
import { ConfirmMutationDialog } from "../confirm-dialog";
import { Alert, ErrorAlert } from "@/components/ui/alert/alert";
import { Button } from "@/components/ui/button/button";
import { Card } from "@/components/ui/card/card";
import { Stack } from "@/components/ui/layout/layout";
import { Loading } from "@/components/ui/spinner/spinner";
import { toast } from "@/components/ui/toast/toast";
import { type AutomationToken, type TokenScope, revokeToken, tokensKey, tokensQuery } from "./api";
import { CreateTokenDialog } from "./create-dialog";
import { maxActiveTokens } from "./labels";
import { TokenTable } from "./token-table";

type Props = {
  scope: TokenScope;
  canCreate: boolean;
  canRevoke: boolean;
  title?: string;
  description?: string;
  /** Why creating isn't possible, when it isn't (an archived team, read-only staff). */
  note?: string;
};

export function TokensPanel({ scope, canCreate, canRevoke, title = "Automation tokens", description, note }: Props) {
  const qc = useQueryClient();
  const list = useQuery(tokensQuery(scope));
  const [creating, setCreating] = useState(false);
  const [revoking, setRevoking] = useState<AutomationToken | null>(null);
  const items = list.data ?? [];
  const active = items.filter((t) => t.status === "active").length;
  const full = active >= maxActiveTokens;
  const revoke = useMutation({
    mutationFn: (t: AutomationToken) => revokeToken(scope, t),
    onSuccess: (_, t) => {
      setRevoking(null);
      toast.success(`${t.name} was revoked`);
    },
    onSettled: () => void qc.invalidateQueries({ queryKey: tokensKey(scope) }),
  });
  const create = canCreate && (
    <Button size="sm" disabled={full} title={full ? `${maxActiveTokens} tokens are active, the most there can be. Revoke one first.` : undefined} onClick={() => setCreating(true)}>
      <Plus aria-hidden /> Create token
    </Button>
  );
  return (
    <Card title={title} description={description} actions={create || undefined}>
      <Stack gap={4}>
        <Alert tone="info">
          Tokens are for scripts and CI. They work on the management API (/v1) only, never on an MCP gateway (/mcp), and are created and revoked here, signed in as a person: a token can&apos;t create or revoke
          tokens. At most {maxActiveTokens} can be active at once; they always expire, after at most 365 days.
        </Alert>
        {full && (
          <Alert tone="warning" title={`${maxActiveTokens} active tokens: the most there can be`}>
            {canCreate ? "Revoke one you no longer use before creating another." : "Revoke one before another can be created."}
          </Alert>
        )}
        {note && <Alert tone="info">{note}</Alert>}
        {list.isLoading ? (
          <Loading label="Loading tokens…" />
        ) : list.error ? (
          <ErrorAlert error={list.error} title="Couldn't load the tokens" onRetry={() => void list.refetch()} />
        ) : (
          <TokenTable
            caption={title}
            items={items}
            onRevoke={canRevoke ? setRevoking : undefined}
            empty={canCreate ? "No tokens yet. Create one for the pipeline or script that needs it." : "No tokens."}
          />
        )}
        {creating && <CreateTokenDialog scope={scope} onClose={() => setCreating(false)} />}
        <ConfirmMutationDialog
          target={revoking}
          onClose={() => setRevoking(null)}
          mutation={{ ...revoke, error: friendly(revoke.error) }}
          onConfirm={(t) => revoke.mutate(t)}
          title={`Revoke ${revoking?.name ?? "token"}?`}
          description="Whatever uses it stops working on its next request. This can't be undone: create a new token if it is still needed."
          confirmLabel="Revoke token"
        />
      </Stack>
    </Card>
  );
}
