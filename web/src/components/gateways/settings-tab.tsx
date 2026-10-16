/*
 * A gateway's Settings tab: name, description and the switch in one form
 * (If-Match on its revision; edits survive a change made elsewhere, AD-01),
 * the slug (never changes) and Delete in the Danger zone. Whoever can't edit
 * reads it, told who can.
 */
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { adminOnly } from "../../lib/terms";
import { ConfirmMutationDialog } from "../confirm-dialog";
import { useRevisionForm } from "../templates/revision-form";
import { DangerAction, DangerZone, SettingsPage, SettingsSection } from "../templates/settings-page";
import { Button } from "@/components/ui/button/button";
import { CopyField } from "@/components/ui/copy-field/copy-field";
import { Field } from "@/components/ui/field/field";
import { Input, Textarea } from "@/components/ui/input/input";
import { Switch } from "@/components/ui/switch/switch";
import { toast } from "@/components/ui/toast/toast";
import type { Gateway } from "./labels";
import { deleteGateway, type GatewayAbilities, type GatewayScope, invalidateGateway, listPath, storeGateway, updateGateway } from "./scope";

type Form = { name: string; description: string; enabled: boolean };
const formOf = (x: Gateway): Form => ({ name: x.name, description: x.description, enabled: x.enabled });
const labels = { name: "Name", description: "Description", enabled: "Enabled" };

type Props = { gateway: Gateway; scope: GatewayScope; abilities: GatewayAbilities };

function readOnlyNote(gateway: Gateway, scope: GatewayScope) {
  if (scope.kind === "team") return "Members can view the gateway's settings. Owners and admins can change them.";
  if (gateway.ownerKind === "team") return "This is a team gateway: the team's owners and admins change its settings. Platform admins can switch it off from the page header.";
  return adminOnly;
}

export function GatewaySettingsTab({ gateway, scope, abilities }: Props) {
  const qc = useQueryClient();
  const saved = formOf(gateway);
  const [form, setForm, revision] = useRevisionForm(saved, gateway.revision, { labels });
  const [submitted, setSubmitted] = useState(false);
  const save = useMutation({
    mutationFn: async () => updateGateway(scope, gateway, { name: form.name.trim(), description: form.description, enabled: form.enabled }),
    onSuccess: (updated) => {
      setSubmitted(false);
      storeGateway(qc, updated);
      toast.success("Gateway settings saved", updated.enabled === gateway.enabled ? undefined : updated.enabled ? "The gateway is on." : "The gateway is off: its endpoint answers “not found”.");
    },
  });
  const canEdit = abilities.canEdit;
  const nameError = submitted && !form.name.trim() ? "Enter a name." : undefined;
  const dirty = JSON.stringify({ ...form, name: form.name.trim() }) !== JSON.stringify(saved);
  return (
    <SettingsPage
      revision={revision}
      canEdit={canEdit}
      readOnlyNote={readOnlyNote(gateway, scope)}
      dirty={dirty}
      saving={save.isPending}
      error={save.error}
      message={nameError ? "Not saved: fix the highlighted field" : undefined}
      onSave={() => {
        setSubmitted(true);
        if (form.name.trim()) save.mutate();
      }}
      onDiscard={() => {
        setForm(saved);
        setSubmitted(false);
      }}
    >
      <SettingsSection title="General" description="How the gateway is named in lists and in My gateways.">
        <Field label="Name" error={nameError}>
          <Input maxLength={100} disabled={!canEdit} value={form.name} onChange={(e) => setForm((f) => ({ ...f, name: e.target.value }))} />
        </Field>
        <Field label="Description" labelHint="Optional">
          <Textarea maxLength={2000} disabled={!canEdit} value={form.description} onChange={(e) => setForm((f) => ({ ...f, description: e.target.value }))} />
        </Field>
        <CopyField label="Slug" name="gateway slug" value={gateway.slug} description="Can't be changed: it's part of the endpoint URL clients use." />
        <Switch
          label="Enabled"
          description="Turned off, the endpoint answers “not found” to everyone. Its capabilities and access stay as they are."
          disabled={!canEdit}
          checked={form.enabled}
          onCheckedChange={(v) => setForm((f) => ({ ...f, enabled: v }))}
        />
      </SettingsSection>
      {canEdit && <DeleteGateway gateway={gateway} scope={scope} />}
    </SettingsPage>
  );
}

function DeleteGateway({ gateway, scope }: { gateway: Gateway; scope: GatewayScope }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [confirming, setConfirming] = useState<Gateway | null>(null);
  const del = useMutation({
    mutationFn: async () => deleteGateway(scope, gateway),
    onSuccess: () => {
      setConfirming(null);
      toast.success(`${gateway.name} was deleted`);
      // Leave first: the record would load again and find nothing.
      void navigate({ ...listPath(scope), replace: true }).then(() => invalidateGateway(qc, gateway));
    },
  });
  return (
    <DangerZone>
      <DangerAction
        title="Delete this gateway"
        description={`Removes ${gateway.name} and its endpoint. Clients connected to it stop working at once. Its call records are kept. This can't be undone.`}
        action={
          <Button variant="danger" onClick={() => setConfirming(gateway)}>
            Delete gateway
          </Button>
        }
      />
      <ConfirmMutationDialog
        target={confirming}
        onClose={() => setConfirming(null)}
        mutation={del}
        onConfirm={() => del.mutate()}
        title={`Delete ${gateway.name}?`}
        description={`Everyone using ${gateway.url} loses access immediately. Its slug becomes free for another gateway.`}
        confirmLabel="Delete gateway"
      />
    </DangerZone>
  );
}
