/*
 * Admin → Internal networks: the internal networks (CIDRs) and host names
 * teams' MCP servers may use (decision 20). Platform admins edit their list
 * as staged changes and save it at once (If-Match; edits survive a change
 * made elsewhere) after a confirmation; auditors read. Entries from the
 * deployment's UPSTREAM_ALLOWED_NETWORKS are shown read-only, and a checker
 * says what the gateway would do with a URL.
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Plus } from "lucide-react";
import { useState } from "react";
import { api, ApiError, ifMatch, unwrap } from "@/api/client";
import { PageSkeleton } from "@/components/page-skeleton";
import { isRevisionConflict, useRevisionForm } from "@/components/templates/revision-form";
import { SettingsPage, SettingsSection } from "@/components/templates/settings-page";
import { ErrorAlert } from "@/components/ui/alert/alert";
import { Button } from "@/components/ui/button/button";
import { AlertDialog } from "@/components/ui/dialog/dialog";
import { Stack } from "@/components/ui/layout/layout";
import { PageHeader } from "@/components/ui/page-header/page-header";
import { toast } from "@/components/ui/toast/toast";
import { adminOnly, terms } from "@/lib/terms";
import s from "../../shared.module.css";
import { useIsPlatformAdmin } from "../hooks";
import { UrlChecker } from "./checker";
import { entryReasonLabels, entryWarnings, listKey, maxEntries, type NetworkEntry, type NetworkSettings, networksKey, normalEntry } from "./entries";
import { EntryDialog } from "./entry-dialog";
import { EntryTable } from "./entry-table";
import n from "./networks.module.css";

export function AdminNetworksPage() {
  const settings = useQuery({ queryKey: networksKey, queryFn: async () => unwrap(await api.GET("/v1/admin/settings/network")) });
  return (
    <Stack gap={6} className={s.page}>
      <PageHeader
        title={terms.internalNetworks}
        description="Internal networks and hosts that teams' MCP servers may use. Public addresses are always allowed over https; loopback, link-local and cloud metadata addresses never are, whatever this list says."
      />
      {settings.isLoading ? (
        <PageSkeleton />
      ) : settings.data ? (
        <>
          <NetworksForm saved={settings.data} />
          <UrlChecker />
        </>
      ) : (
        <ErrorAlert error={settings.error} title="Couldn't load the internal networks" onRetry={() => void settings.refetch()} />
      )}
    </Stack>
  );
}

/** A 422 invalid_entry as the row it's about and why. */
function entryProblem(err: unknown): { index: number; message: string } | undefined {
  if (!(err instanceof ApiError) || err.code !== "invalid_entry" || typeof err.details?.index !== "number") return undefined;
  const reason = typeof err.details.reason === "string" ? err.details.reason : "";
  return { index: err.details.index, message: entryReasonLabels[reason] ?? err.message };
}

function NetworksForm({ saved }: { saved: NetworkSettings }) {
  const qc = useQueryClient();
  const isAdmin = useIsPlatformAdmin();
  const [editing, setEditing] = useState<number | "new" | null>(null);
  const [confirming, setConfirming] = useState(false);
  const base = { entries: saved.entries.map(normalEntry) };
  const [form, setForm, revision] = useRevisionForm(base, saved.revision, { labels: { entries: "Allowed networks and hosts" } });
  const save = useMutation({
    mutationFn: async () => unwrap(await api.PUT("/v1/admin/settings/network", { params: { header: ifMatch(saved.revision) }, body: { entries: form.entries } })),
    // A failure shows on the save bar, which also handles a change made elsewhere (412).
    onSettled: () => setConfirming(false),
    // The latest list, so the conflict notice can say what changed (main.tsx also refetches everything).
    onError: (err) => {
      if (isRevisionConflict(err)) void qc.invalidateQueries({ queryKey: networksKey });
    },
    onSuccess: (st) => {
      // The server's canonical values (masked networks) replace what was typed.
      setForm({ entries: st.entries.map(normalEntry) });
      qc.setQueryData(networksKey, st);
      toast.success("Internal networks saved", "This replica applies them now; others within 30 seconds.");
    },
  });
  const entries = form.entries;
  const setEntries = (next: NetworkEntry[]) => {
    if (save.error) save.reset();
    setForm({ entries: next });
  };
  const warnings = entries.flatMap((e) => entryWarnings(e).map((w) => `${e.value}: ${w}`));
  const removed = base.entries.filter((b) => !entries.some((e) => e.kind === b.kind && e.value === b.value)).length;
  const add = isAdmin && (
    <Button size="sm" variant="secondary" disabled={entries.length >= maxEntries} onClick={() => setEditing("new")}>
      <Plus aria-hidden /> Add network or host
    </Button>
  );
  // The dialogs sit outside the settings form: a submit inside one must not reach it.
  return (
    <>
      <SettingsPage
        revision={revision}
        canEdit={isAdmin}
        readOnlyNote={adminOnly}
        dirty={listKey(entries) !== listKey(base.entries)}
        saving={save.isPending}
        error={save.error}
        onSave={() => setConfirming(true)}
        onDiscard={() => setEntries(base.entries)}
      >
        <SettingsSection
          title="Allowed networks and hosts"
          description={`A network allows servers at internal addresses inside it; a host name allows the internal addresses it resolves to. Plain http needs "Allow plain http". At most ${maxEntries}.`}
          actions={add || undefined}
        >
          <EntryTable
            caption="Allowed networks and hosts"
            entries={entries}
            empty={
              isAdmin
                ? "Nothing yet: MCP servers must be at public addresses. Add a network or host to allow internal servers."
                : "Nothing yet: MCP servers must be at public addresses."
            }
            onEdit={isAdmin ? (i) => setEditing(i) : undefined}
            onRemove={isAdmin ? (i) => setEntries(entries.filter((_, j) => j !== i)) : undefined}
            problem={entryProblem(save.error)}
          />
        </SettingsSection>
        <SettingsSection title="From deployment config" description="Set by the deployment in UPSTREAM_ALLOWED_NETWORKS. Change them there; they can't be edited here.">
          <EntryTable caption="From deployment config" entries={saved.deploymentEntries} empty="None: UPSTREAM_ALLOWED_NETWORKS isn't set." />
        </SettingsSection>
      </SettingsPage>
      <AlertDialog
        open={confirming}
        onOpenChange={(o) => {
          if (!o) setConfirming(false);
        }}
        tone="primary"
        title="Save the internal networks?"
        description={`${entries.length} ${entries.length === 1 ? "entry" : "entries"}. Servers are never changed: one outside the new list fails its next connection (not allowed) and shows as failing. Other replicas apply the list within 30 seconds.`}
        confirmLabel="Save networks"
        busy={save.isPending}
        onConfirm={() => save.mutate()}
      >
        {removed > 0 && <p>{removed === 1 ? "1 entry is removed." : `${removed} entries are removed.`} Servers that relied on it stop working.</p>}
        {warnings.length > 0 && (
          <ul className={n.warnings}>
            {warnings.map((w) => (
              <li key={w}>{w}</li>
            ))}
          </ul>
        )}
      </AlertDialog>
      {editing !== null && (
        <EntryDialog
          entry={editing === "new" ? undefined : entries[editing]}
          others={entries.filter((_, j) => j !== editing)}
          onClose={() => setEditing(null)}
          onSave={(e) => {
            setEntries(editing === "new" ? [...entries, e] : entries.map((x, j) => (j === editing ? e : x)));
            setEditing(null);
          }}
        />
      )}
    </>
  );
}
