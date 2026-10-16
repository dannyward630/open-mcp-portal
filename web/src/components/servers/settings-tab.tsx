/*
 * A server's Settings tab: one form over the server (If-Match on its
 * revision; edits survive a change made elsewhere, AD-01), the replace-only
 * header value, and Delete in the Danger zone (owners and admins). Members
 * and Admin read it: Admin never changes a team's server settings.
 *
 * A new URL is read again at once. Under approval_required, a published
 * server goes back to review (pending): the form says so before the save,
 * and the saved message after it.
 */
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { api, ifMatch, unwrap } from "../../api/client";
import { useAuthConfig } from "../../session";
import { ConfirmMutationDialog } from "../confirm-dialog";
import { Alert } from "@/components/ui/alert/alert";
import { useRevisionForm } from "../templates/revision-form";
import { DangerAction, DangerZone, SettingsPage, SettingsSection } from "../templates/settings-page";
import { Button } from "@/components/ui/button/button";
import { CopyField } from "@/components/ui/copy-field/copy-field";
import { Field } from "@/components/ui/field/field";
import { Input, Textarea } from "@/components/ui/input/input";
import { toast } from "@/components/ui/toast/toast";
import type { McpServer } from "./labels";
import { invalidateServer, type ServerAbilities, type ServerScope, storeServer } from "./scope";
import { type AuthEdit, AuthFields, EnabledSwitch, type ServerForm, serverFieldErrors, serverProblems, TimeoutField, UrlField } from "./server-fields";

const formOf = (x: McpServer): ServerForm => ({
  name: x.name,
  description: x.description,
  url: x.url,
  authHeaderName: x.authHeaderName ?? "",
  timeoutSeconds: x.timeoutSeconds,
  enabled: x.enabled,
});
const labels = { name: "Name", description: "Description", url: "URL", authHeaderName: "Header name", timeoutSeconds: "Timeout (seconds)", enabled: "Enabled" };
const noAuthEdit: AuthEdit = { value: "", remove: false };

type Props = { server: McpServer; scope: ServerScope; abilities: ServerAbilities };

/** The saved message: what a new URL did (read again; back to review). */
function savedMessage(before: McpServer, after: McpServer, urlChanged: boolean) {
  if (before.publishState === "published" && after.publishState === "pending") {
    toast.success("Server settings saved", `${after.name} left the catalog until a platform admin reviews its new URL.`);
  } else if (urlChanged && after.lastRefreshError) {
    toast.warning("Server settings saved", "Its capabilities couldn't be read from the new URL. See the Overview tab.");
  } else {
    toast.success("Server settings saved", urlChanged ? "Its capabilities were read again from the new URL." : undefined);
  }
}

export function ServerSettingsTab({ server, scope, abilities }: Props) {
  const qc = useQueryClient();
  const dev = Boolean(useAuthConfig().data?.devAuthEnabled);
  const saved = formOf(server);
  const [form, setForm, revision] = useRevisionForm(saved, server.revision, { labels });
  const [auth, setAuth] = useState<AuthEdit>(noAuthEdit);
  const [submitted, setSubmitted] = useState(false);
  const save = useMutation({
    mutationFn: async (_: { before: McpServer; urlChanged: boolean }) => {
      const name = form.authHeaderName.trim();
      const headerChanged = name !== (server.authHeaderName ?? "");
      const authBody = auth.remove ? { authHeaderName: "" } : { ...(headerChanged || auth.value ? { authHeaderName: name } : {}), ...(auth.value ? { authValue: auth.value } : {}) };
      return unwrap(
        await api.PATCH("/v1/teams/{team}/mcp-servers/{serverId}", {
          params: { path: { team: server.teamSlug, serverId: server.id }, header: ifMatch(server.revision) },
          body: { name: form.name.trim(), description: form.description, url: form.url.trim(), timeoutSeconds: form.timeoutSeconds, enabled: form.enabled, ...authBody },
        }),
      );
    },
    onSuccess: (updated, { before, urlChanged }) => {
      setAuth(noAuthEdit);
      setSubmitted(false);
      storeServer(qc, updated);
      savedMessage(before, updated, urlChanged);
    },
  });
  const canEdit = abilities.canEdit && scope.kind === "team";
  const dirty = JSON.stringify({ ...form, name: form.name.trim(), url: form.url.trim() }) !== JSON.stringify(saved) || Boolean(auth.value) || auth.remove;
  const problems = serverProblems(form, auth, { dev, hasAuth: server.hasAuth });
  const invalid = Object.keys(problems).length > 0;
  const shown = submitted ? problems : {};
  const refused = serverFieldErrors(save.error).url;
  const errors = { ...shown, url: shown.url ?? refused };
  const set = (patch: Partial<ServerForm>) => {
    // The refusal was for the URL that was sent: editing it clears it.
    if (patch.url !== undefined && refused) save.reset();
    setForm((f) => ({ ...f, ...patch }));
  };
  const fields = { form, set, errors, dev, disabled: !canEdit };
  const urlChanged = form.url.trim() !== server.url;
  const leavesCatalog = canEdit && urlChanged && server.publishState === "published" && server.effectivePublishPolicy === "approval_required";

  return (
    <SettingsPage
      revision={revision}
      canEdit={canEdit}
      readOnlyNote={
        scope.kind === "admin"
          ? "You can view these settings. The team's editors, admins and owners change them."
          : "Members can view the server's settings. Editors, admins and owners can change them."
      }
      dirty={dirty}
      saving={save.isPending}
      error={errors.url ? undefined : save.error}
      onSave={() => {
        setSubmitted(true);
        if (!invalid) save.mutate({ before: server, urlChanged });
      }}
      onDiscard={() => {
        setForm(saved);
        setAuth(noAuthEdit);
        setSubmitted(false);
      }}
      message={submitted && invalid ? "Not saved: fix the highlighted fields" : leavesCatalog ? "Saving takes the server out of the catalog until it's reviewed again" : undefined}
    >
      <SettingsSection title="General" description="How the server is named in the team's lists and, once published, in the catalog.">
        <Field label="Name" error={errors.name}>
          <Input maxLength={100} disabled={!canEdit} value={form.name} onChange={(e) => fields.set({ name: e.target.value })} />
        </Field>
        <Field label="Description" labelHint="Optional">
          <Textarea maxLength={2000} disabled={!canEdit} value={form.description} onChange={(e) => fields.set({ description: e.target.value })} />
        </Field>
        <CopyField label="Slug" name="server slug" value={server.slug} description="Can't be changed. Gateways name the server's tools slug__tool." />
      </SettingsSection>
      <SettingsSection title="Connection" description="A new URL is read again when you save. After changing only the header, press Refresh on the Overview tab.">
        <UrlField {...fields} />
        {leavesCatalog && (
          <Alert tone="warning" title="The server will leave the catalog">
            Your team needs approval to publish, so a new URL is reviewed again: once you save, {server.name} is out of the company catalog until a platform admin approves it.
          </Alert>
        )}
        <TimeoutField {...fields} />
        <EnabledSwitch {...fields} />
      </SettingsSection>
      <SettingsSection title="Authentication" description="One static header sent with every request. Its value is stored encrypted; only its last four characters are ever shown.">
        <AuthFields {...fields} auth={auth} setAuth={setAuth} hasAuth={server.hasAuth} hint={server.authValueHint} />
      </SettingsSection>
      {scope.kind === "team" && abilities.canManage && <DeleteServer server={server} />}
    </SettingsPage>
  );
}

function DeleteServer({ server }: { server: McpServer }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [confirming, setConfirming] = useState<McpServer | null>(null);
  const del = useMutation({
    mutationFn: async () => unwrap(await api.DELETE("/v1/teams/{team}/mcp-servers/{serverId}", { params: { path: { team: server.teamSlug, serverId: server.id } } })),
    onSuccess: () => {
      setConfirming(null);
      toast.success(`${server.name} was deleted`);
      // Leave first: the record would load again and find nothing.
      void navigate({ to: "/teams/$team/servers", params: { team: server.teamSlug }, replace: true }).then(() => invalidateServer(qc, server));
    },
  });
  return (
    <DangerZone>
      <DangerAction
        title="Delete this server"
        description={`Removes ${server.name} and everything read from it${server.publishState === "published" ? ", and takes it off the catalog" : ""}. This can't be undone.`}
        action={
          <Button variant="danger" onClick={() => setConfirming(server)}>
            Delete server
          </Button>
        }
      />
      <ConfirmMutationDialog
        target={confirming}
        onClose={() => setConfirming(null)}
        mutation={del}
        onConfirm={() => del.mutate()}
        title={`Delete ${server.name}?`}
        description="Its capabilities, health checks and catalog entry are deleted. Its slug becomes free for another server."
        confirmLabel="Delete server"
      />
    </DangerZone>
  );
}
