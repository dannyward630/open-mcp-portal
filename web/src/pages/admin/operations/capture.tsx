/*
 * Admin → Payload capture: how recorded call contents are redacted
 * (decision 32). The built-in rules and sensitive member names always
 * apply; platform admins add their own patterns as staged edits, saved at
 * once with If-Match (edits survive a change made elsewhere). A refused
 * pattern is shown on its row. The tester tries a sample against the form's
 * patterns. Auditors read and test. Whether a gateway or server records is
 * set on it, by its team.
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Pencil, Plus, Trash2 } from "lucide-react";
import { Fragment, useState } from "react";
import { api, ifMatch, type Schemas, unwrap } from "@/api/client";
import { captureWho } from "@/components/capture";
import { PageSkeleton } from "@/components/page-skeleton";
import { ActionMenu } from "@/components/templates/action-menu";
import { isRevisionConflict, useRevisionForm } from "@/components/templates/revision-form";
import { SettingsPage, SettingsSection } from "@/components/templates/settings-page";
import { ErrorAlert } from "@/components/ui/alert/alert";
import { Badge } from "@/components/ui/badge/badge";
import { Button } from "@/components/ui/button/button";
import { CellText } from "@/components/ui/data-table/data-table";
import { Stack } from "@/components/ui/layout/layout";
import { PageHeader } from "@/components/ui/page-header/page-header";
import { Table, Td, Tr } from "@/components/ui/table/table";
import { toast } from "@/components/ui/toast/toast";
import { adminOnly, terms } from "@/lib/terms";
import s from "../../shared.module.css";
import { useIsPlatformAdmin } from "../hooks";
import { refusedPattern } from "./capture-labels";
import { maxPatterns, type Pattern, PatternDialog } from "./pattern-dialog";
import { RedactionTester } from "./redaction-tester";
import o from "./operations.module.css";

type Settings = Schemas["CaptureSettings"];

export const captureKey = ["admin", "capture"];

export function AdminCapturePage() {
  const settings = useQuery({ queryKey: captureKey, queryFn: async () => unwrap(await api.GET("/v1/admin/settings/capture")) });
  return (
    <Stack gap={6} className={s.page}>
      <PageHeader
        title={terms.payloadCapture}
        description={`Gateways and servers whose team turned on "Record call contents" keep each call's arguments and result, redacted and cut to 16 KiB per part. ${captureWho}`}
      />
      {settings.isLoading ? (
        <PageSkeleton />
      ) : settings.data ? (
        <CaptureForm saved={settings.data} />
      ) : (
        <ErrorAlert error={settings.error} title="Couldn't load the capture settings" onRetry={() => void settings.refetch()} />
      )}
    </Stack>
  );
}

const patternsText = (v: unknown) => (Array.isArray(v) ? (v as Pattern[]).map((p) => p.pattern).join(", ") || "none" : "");

function CaptureForm({ saved }: { saved: Settings }) {
  const qc = useQueryClient();
  const isAdmin = useIsPlatformAdmin();
  const [editing, setEditing] = useState<number | "new" | null>(null);
  const base = { patterns: saved.patterns };
  const [form, setForm, revision] = useRevisionForm(base, saved.revision, { labels: { patterns: "Patterns" }, format: (_k, v) => patternsText(v) });
  const save = useMutation({
    mutationFn: async () => unwrap(await api.PUT("/v1/admin/settings/capture", { params: { header: ifMatch(saved.revision) }, body: { patterns: form.patterns } })),
    onError: (err) => {
      if (isRevisionConflict(err)) void qc.invalidateQueries({ queryKey: captureKey });
    },
    onSuccess: (st) => {
      setForm({ patterns: st.patterns });
      qc.setQueryData(captureKey, st);
      toast.success("Redaction patterns saved", "They apply to calls recorded from now on (every replica within 30 seconds). Stored recordings aren't changed.");
    },
  });
  const patterns = form.patterns;
  const setPatterns = (next: Pattern[]) => {
    if (save.error) save.reset();
    setForm({ patterns: next });
  };
  const refused = refusedPattern(save.error);
  const dirty = JSON.stringify(patterns) !== JSON.stringify(base.patterns);
  return (
    <>
      <SettingsPage
        revision={revision}
        canEdit={isAdmin}
        readOnlyNote={adminOnly}
        dirty={dirty}
        saving={save.isPending}
        error={refused ? undefined : save.error}
        message={refused ? "Not saved: fix the highlighted pattern" : undefined}
        onSave={() => save.mutate()}
        onDiscard={() => setPatterns(base.patterns)}
      >
        <SettingsSection title="Built-in rules" description="Always on. Values that look like these are replaced with [REDACTED] in every recorded string.">
          <dl className={o.rules}>
            {saved.builtInRules.map((r) => (
              <Fragment key={r.name}>
                <dt>{r.name}</dt>
                <dd>{r.description}</dd>
              </Fragment>
            ))}
          </dl>
        </SettingsSection>
        <SettingsSection title="Sensitive member names" description="JSON members with these names (compared in lower case, without - and _) always have their value replaced.">
          <div className={o.chips}>
            {saved.sensitiveKeys.map((k) => (
              <Badge key={k} size="sm" variant="outline">
                {k}
              </Badge>
            ))}
          </div>
        </SettingsSection>
        <SettingsSection
          title="Your patterns"
          description={`Extra regular expressions for your organisation's secrets or personal data (employee numbers, account IDs). At most ${maxPatterns}.`}
          actions={
            isAdmin ? (
              <Button size="sm" variant="secondary" disabled={patterns.length >= maxPatterns} onClick={() => setEditing("new")}>
                <Plus aria-hidden /> Add pattern
              </Button>
            ) : undefined
          }
        >
          <Table
            caption="Redaction patterns"
            stack
            columns={["Pattern", "Note", ...(isAdmin ? [{ label: "", stickyEnd: true }] : [])]}
            empty={patterns.length === 0 ? <p className={s.muted}>None yet: only the built-in rules apply.</p> : undefined}
          >
            {patterns.map((p, i) => (
              <Tr key={`${i} ${p.pattern}`}>
                <Td>
                  <CellText wrap="anywhere" primary={<code className={s.mono}>{p.pattern}</code>} />
                  {refused?.index === i && <span className={s.dangerText}>{refused.message}</span>}
                </Td>
                <Td>{p.note ? <CellText wrap="anywhere" primary={p.note} /> : <span className={s.muted}>—</span>}</Td>
                {isAdmin && (
                  <Td stickyEnd>
                    <ActionMenu
                      label={`Actions for ${p.pattern}`}
                      actions={[
                        { label: "Edit…", icon: <Pencil aria-hidden />, onSelect: () => setEditing(i) },
                        { label: "Remove", icon: <Trash2 aria-hidden />, danger: true, onSelect: () => setPatterns(patterns.filter((_, j) => j !== i)) },
                      ]}
                    />
                  </Td>
                )}
              </Tr>
            ))}
          </Table>
        </SettingsSection>
      </SettingsPage>
      {/* Outside the settings form: their own submits must not reach it. */}
      <RedactionTester patterns={patterns} edited={dirty} />
      {editing !== null && (
        <PatternDialog
          pattern={editing === "new" ? undefined : patterns[editing]}
          others={patterns.filter((_, j) => j !== editing)}
          onClose={() => setEditing(null)}
          onSave={(p) => {
            setPatterns(editing === "new" ? [...patterns, p] : patterns.map((x, j) => (j === editing ? p : x)));
            setEditing(null);
          }}
        />
      )}
    </>
  );
}
