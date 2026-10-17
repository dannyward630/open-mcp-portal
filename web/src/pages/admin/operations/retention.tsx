/*
 * Admin → Retention: how long call records, recorded call contents, the
 * audit log and usage totals are kept (decision 33), each within its
 * bounds, and what the last run of the retention loop deleted. Teams may
 * have shorter or longer periods for the first two (on their admin page).
 * Staged edits, one Save with If-Match; a shorter period asks first, since
 * the next run deletes what's past it. Auditors read.
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { api, ifMatch, type Schemas, unwrap } from "@/api/client";
import { PageSkeleton } from "@/components/page-skeleton";
import { isRevisionConflict, useRevisionForm } from "@/components/templates/revision-form";
import { SettingsPage, SettingsSection } from "@/components/templates/settings-page";
import { ErrorAlert } from "@/components/ui/alert/alert";
import { AlertDialog } from "@/components/ui/dialog/dialog";
import { Field } from "@/components/ui/field/field";
import { Stack } from "@/components/ui/layout/layout";
import { NumberInput } from "@/components/ui/number-input/number-input";
import { PageHeader } from "@/components/ui/page-header/page-header";
import { Table, Td, Tr } from "@/components/ui/table/table";
import { TextLink } from "@/components/ui/text-link/text-link";
import { toast } from "@/components/ui/toast/toast";
import { daysProblem, daysText, type RetentionKind, retentionLabels, retentionSettingsKey, retentionSettingsQuery } from "@/lib/retention";
import { adminOnly, terms } from "@/lib/terms";
import s from "../../shared.module.css";
import { useIsPlatformAdmin } from "../hooks";
import { LastRun } from "./retention-run";
import o from "./operations.module.css";

type Settings = Schemas["RetentionSettings"];
type Period = Schemas["RetentionPeriod"];
type Form = Partial<Record<RetentionKind, string>>;

const formOf = (st: Settings): Form => Object.fromEntries(st.periods.map((p) => [p.kind, String(p.days)]));

export function AdminRetentionPage() {
  const settings = useQuery(retentionSettingsQuery());
  return (
    <Stack gap={6} className={s.page}>
      <PageHeader
        title={terms.retention}
        description="How long records are kept before the retention job deletes them, in batches, every 15 minutes or so. A call involving two teams is kept for the shorter of their periods."
      />
      {settings.isLoading ? (
        <PageSkeleton />
      ) : settings.data ? (
        <>
          <RetentionForm saved={settings.data} />
          <LastRun run={settings.data.lastRun} />
        </>
      ) : (
        <ErrorAlert error={settings.error} title="Couldn't load the retention periods" onRetry={() => void settings.refetch()} />
      )}
    </Stack>
  );
}

/** The kinds whose period got shorter. */
export function shortened(periods: { kind: RetentionKind; days: number }[], next: { kind: RetentionKind; days: number }[]) {
  return next.filter((n) => {
    const p = periods.find((x) => x.kind === n.kind);
    return p && n.days < p.days;
  });
}

function RetentionForm({ saved }: { saved: Settings }) {
  const qc = useQueryClient();
  const isAdmin = useIsPlatformAdmin();
  const [confirming, setConfirming] = useState(false);
  const [submitted, setSubmitted] = useState(false);
  const base = formOf(saved);
  const labels = Object.fromEntries(saved.periods.map((p) => [p.kind, retentionLabels[p.kind].label]));
  const [form, setForm, revision] = useRevisionForm<Form>(base, saved.revision, { labels, format: (_k, v) => (v ? `${v} days` : "empty") });
  const errors = Object.fromEntries(saved.periods.map((p) => [p.kind, daysProblem(form[p.kind] ?? "", p.minDays, p.maxDays)]));
  const invalid = Object.values(errors).some(Boolean);
  const changes = invalid ? [] : saved.periods.flatMap((p) => (Number(form[p.kind]) !== p.days ? [{ kind: p.kind, days: Number(form[p.kind]) }] : []));
  const sooner = shortened(saved.periods, changes);
  const save = useMutation({
    mutationFn: async () => unwrap(await api.PUT("/v1/admin/settings/retention", { params: { header: ifMatch(saved.revision) }, body: { periods: changes } })),
    onSettled: () => setConfirming(false),
    onError: (err) => {
      if (isRevisionConflict(err)) void qc.invalidateQueries({ queryKey: retentionSettingsKey });
    },
    onSuccess: (st) => {
      setSubmitted(false);
      qc.setQueryData(retentionSettingsKey, st);
      void qc.invalidateQueries({ queryKey: ["admin", "team"] });
      void qc.invalidateQueries({ queryKey: ["team"] });
      toast.success("Retention periods saved", "The next run applies them.");
    },
  });
  return (
    <>
      <SettingsPage
        revision={revision}
        canEdit={isAdmin}
        readOnlyNote={adminOnly}
        dirty={JSON.stringify(form) !== JSON.stringify(base)}
        saving={save.isPending}
        error={save.error}
        saveDisabled={submitted && invalid}
        message={invalid ? "Not saved: fix the highlighted periods" : undefined}
        onSave={() => {
          setSubmitted(true);
          if (invalid || changes.length === 0) return;
          if (sooner.length > 0) setConfirming(true);
          else save.mutate();
        }}
        onDiscard={() => {
          setForm(base);
          setSubmitted(false);
        }}
      >
        <SettingsSection
          title="Periods"
          description={
            <>
              Teams can have their own period for call records and recorded contents, set on their page in{" "}
              <TextLink render={<Link to="/admin/teams" />}>Teams</TextLink>.
            </>
          }
        >
          <Table caption="Retention periods" stack columns={["Records", { label: "Keep for", width: "14rem" }, { label: "Allowed", width: "10rem" }]}>
            {saved.periods.map((p) => (
              <PeriodRow
                key={p.kind}
                p={p}
                value={form[p.kind] ?? ""}
                error={submitted || form[p.kind] !== String(p.days) ? errors[p.kind] : undefined}
                isAdmin={isAdmin}
                onChange={(v) => {
                  if (save.error) save.reset();
                  setForm((f) => ({ ...f, [p.kind]: v }));
                }}
              />
            ))}
          </Table>
        </SettingsSection>
      </SettingsPage>
      <AlertDialog
        open={confirming}
        onOpenChange={(open) => {
          if (!open) setConfirming(false);
        }}
        title="Delete records sooner?"
        description="The next run permanently deletes what's older than the shorter periods. This can't be undone."
        confirmLabel="Save periods"
        busy={save.isPending}
        onConfirm={() => save.mutate()}
      >
        <ul className={o.list}>
          {sooner.map((c) => (
            <li key={c.kind}>{`${retentionLabels[c.kind].label}: ${daysText(saved.periods.find((p) => p.kind === c.kind)!.days)} → ${daysText(c.days)}`}</li>
          ))}
        </ul>
      </AlertDialog>
    </>
  );
}

function PeriodRow({ p, value, error, isAdmin, onChange }: { p: Period; value: string; error?: string; isAdmin: boolean; onChange: (v: string) => void }) {
  const info = retentionLabels[p.kind];
  return (
    <Tr>
      <Td>
        <span className={s.primary}>{info.label}</span>
        <span className={s.secondary}>{info.description}</span>
        {p.days !== p.builtInDays && <span className={s.secondary}>Built-in default: {daysText(p.builtInDays)}</span>}
      </Td>
      <Td>
        {isAdmin ? (
          <Field label={`${info.label}: days`} hideLabel error={error} className={o.amountField}>
            <NumberInput size="sm" maximumFractionDigits={0} value={value} unit="days" onValueChange={onChange} />
          </Field>
        ) : (
          daysText(p.days)
        )}
      </Td>
      <Td muted>
        {`${p.minDays.toLocaleString()}–${p.maxDays.toLocaleString()} days`}
        {p.teamOverridable && <span className={s.secondary}>Teams can override</span>}
      </Td>
    </Tr>
  );
}

