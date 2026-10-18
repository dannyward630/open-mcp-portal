/*
 * Webhook destinations of a team (Team settings › Notifications) or of the platform (Admin › Notifications): the
 * same panel for both. Add (a dialog; the signing secret of a JSON destination is shown once), turn on and off,
 * Send test, Rotate secret, Delivery log, Change and Delete (asks first). A destination stopped after repeated
 * failures gets a banner with Turn back on. At most 10 per scope. Readers (platform staff on a team, auditors) see
 * the list and the delivery log, nothing that changes.
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Plus } from "lucide-react";
import { useState } from "react";
import { friendly } from "../../../lib/error-wording";
import { ConfirmMutationDialog } from "../../confirm-dialog";
import { V02ErrorAlert } from "../../errors";
import { Alert, ErrorAlert } from "@/components/ui/alert/alert";
import { Button } from "@/components/ui/button/button";
import { Card } from "@/components/ui/card/card";
import { Stack } from "@/components/ui/layout/layout";
import { Loading } from "@/components/ui/spinner/spinner";
import { toast } from "@/components/ui/toast/toast";
import { type Destination, type DestinationScope, deleteDestination, destinationsKey, destinationsQuery, updateDestination } from "./api";
import { DeliveriesDialog } from "./deliveries-dialog";
import { DestinationTable } from "./destination-table";
import { DestinationForm } from "./form-dialog";
import { stoppedByFailures } from "./labels";
import { RotateDialog } from "./rotate-dialog";
import { SecretDialog } from "./secret-dialog";
import { TestDialog } from "./test-dialog";

/** The most destinations a scope can have. */
export const maxDestinations = 10;

type Secret = { title: string; secret: string; previousUntil?: string | null };

type Props = {
  scope: DestinationScope;
  canManage: boolean;
  title?: string;
  description?: string;
  /** Shown instead of "Add destination" when adding isn't possible (an archived team). */
  readOnlyNote?: string;
};

export function DestinationsPanel({ scope, canManage, title = "Webhook destinations", description, readOnlyNote }: Props) {
  const qc = useQueryClient();
  const list = useQuery(destinationsQuery(scope));
  const [form, setForm] = useState<Destination | "new" | null>(null);
  const [secret, setSecret] = useState<Secret | null>(null);
  const [testing, setTesting] = useState<Destination | null>(null);
  const [rotating, setRotating] = useState<Destination | null>(null);
  const [deleting, setDeleting] = useState<Destination | null>(null);
  const [log, setLog] = useState<Destination | null>(null);
  const items = list.data ?? [];
  const refresh = () => void qc.invalidateQueries({ queryKey: destinationsKey(scope) });
  const toggle = useMutation({
    mutationFn: (v: { d: Destination; enabled: boolean }) => updateDestination(scope, v.d, { enabled: v.enabled }),
    onSettled: refresh,
  });
  const remove = useMutation({
    mutationFn: (d: Destination) => deleteDestination(scope, d),
    onSuccess: (_, d) => {
      setDeleting(null);
      toast.success(`${d.name} was deleted`);
      refresh();
    },
  });
  const full = items.length >= maxDestinations;
  const stopped = items.filter(stoppedByFailures);
  const add = canManage && (
    <Button size="sm" disabled={full} title={full ? `At most ${maxDestinations} destinations. Delete one first.` : undefined} onClick={() => setForm("new")}>
      <Plus aria-hidden /> Add destination
    </Button>
  );
  return (
    <Card title={title} description={description} actions={add || undefined}>
      <Stack gap={4}>
        {stopped.map((d) => (
          <Alert
            key={d.id}
            tone="warning"
            title={`${d.name} was stopped`}
            actions={
              canManage && (
                <Button size="sm" variant="secondary" loading={toggle.isPending && toggle.variables?.d.id === d.id} onClick={() => toggle.mutate({ d, enabled: true })}>
                  Turn back on
                </Button>
              )
            }
          >
            It failed 20 deliveries in a row, so the portal stopped sending to it. Check the delivery log and the receiver, then turn it back on.
          </Alert>
        ))}
        {readOnlyNote && <Alert tone="info">{readOnlyNote}</Alert>}
        {list.isLoading ? (
          <Loading label="Loading destinations…" />
        ) : list.error ? (
          <ErrorAlert error={list.error} title="Couldn't load the destinations" onRetry={() => void list.refetch()} />
        ) : (
          <DestinationTable
            items={items}
            canManage={canManage}
            empty={canManage ? "No destinations yet. Add one to send these events to a chat channel or your own receiver." : "No destinations."}
            handlers={{
              toggling: toggle.isPending ? toggle.variables?.d.id : undefined,
              onToggle: (d, enabled) => toggle.mutate({ d, enabled }),
              onTest: setTesting,
              onLog: setLog,
              onEdit: setForm,
              onRotate: setRotating,
              onDelete: setDeleting,
            }}
          />
        )}
        <V02ErrorAlert error={toggle.error} title="Couldn't change the destination" />
        {form && (
          <DestinationForm
            scope={scope}
            destination={form === "new" ? undefined : form}
            onClose={() => setForm(null)}
            onCreated={(c) => {
              setForm(null);
              if (c.secret) setSecret({ title: `${c.destination.name} was added`, secret: c.secret });
              else toast.success(`${c.destination.name} was added`, "Slack destinations aren't signed, so there is no secret.");
            }}
          />
        )}
        {secret && <SecretDialog title={secret.title} secret={secret.secret} previousUntil={secret.previousUntil} onClose={() => setSecret(null)} />}
        {testing && <TestDialog scope={scope} destination={testing} onClose={() => setTesting(null)} />}
        {log && <DeliveriesDialog scope={scope} destination={log} onClose={() => setLog(null)} />}
        {rotating && (
          <RotateDialog
            scope={scope}
            destination={rotating}
            onClose={() => setRotating(null)}
            onRotated={(d, s) => {
              setRotating(null);
              setSecret({ title: `New signing secret for ${d.name}`, secret: s, previousUntil: d.previousSecretExpiresAt });
            }}
          />
        )}
        <ConfirmMutationDialog
          target={deleting}
          onClose={() => setDeleting(null)}
          mutation={{ ...remove, error: friendly(remove.error) }}
          onConfirm={(d) => remove.mutate(d)}
          title={`Delete ${deleting?.name ?? "destination"}?`}
          description="Nothing more is sent to it and its delivery log is deleted. Deliveries still waiting for it are dropped. This can't be undone."
          confirmLabel="Delete destination"
        />
      </Stack>
    </Card>
  );
}
