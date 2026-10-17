/*
 * Admin team › Limits & retention: the team's own rate limits (service
 * accounts, and the default of its gateways) and retention periods (call
 * records, recorded contents), each inheriting the platform value or set,
 * with what applies. One form and Save; the overrides share the team's
 * revision (If-Match), so a save sends the limits, then the periods with
 * the revision the first returned. A shorter period asks first. Auditors read.
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { api, ifMatch, unwrap } from "@/api/client";
import { isRevisionConflict, useRevisionForm } from "@/components/templates/revision-form";
import { SettingsPage, SettingsSection } from "@/components/templates/settings-page";
import { ErrorAlert } from "@/components/ui/alert/alert";
import { AlertDialog } from "@/components/ui/dialog/dialog";
import { SkeletonText } from "@/components/ui/skeleton/skeleton";
import { TextLink } from "@/components/ui/text-link/text-link";
import { toast } from "@/components/ui/toast/toast";
import { adminTeamLimitsQuery } from "@/lib/limits";
import { adminTeamRetentionQuery, daysText, retentionLabels } from "@/lib/retention";
import { adminOnly } from "@/lib/terms";
import { LimitsTable, PeriodsTable } from "./overrides-tables";
import {
  blank,
  effectivePeriod,
  limitField,
  type OverridesForm,
  overrideChanges,
  overridesFormOf,
  overridesInvalid,
  periodField,
  refusedOverride,
  shorterPeriods,
  type TeamLimit,
  type TeamPeriod,
} from "./overrides-form";

export function TeamLimitsTab({ team, name, isAdmin }: { team: string; name: string; isAdmin: boolean }) {
  const limits = useQuery(adminTeamLimitsQuery(team));
  const retention = useQuery(adminTeamRetentionQuery(team));
  if (limits.isLoading || retention.isLoading) return <SkeletonText lines={6} />;
  if (!limits.data || !retention.data) {
    const error = limits.error ?? retention.error;
    return <ErrorAlert error={error} title="Couldn't load the team's limits and retention" onRetry={() => void Promise.all([limits.refetch(), retention.refetch()])} />;
  }
  return (
    <OverridesSettings
      team={team}
      name={name}
      isAdmin={isAdmin}
      limits={limits.data.items}
      periods={retention.data.periods}
      revision={Math.max(limits.data.revision, retention.data.revision)}
    />
  );
}

/** An override in the list of changes made elsewhere: "inherit" or its value. */
const overrideText = (v: unknown) => {
  const r = v as { mode?: string; value?: string } | undefined;
  return !r || r.mode === "inherit" ? "inherit" : (r.value ?? "");
};

type Props = { team: string; name: string; isAdmin: boolean; limits: TeamLimit[]; periods: TeamPeriod[]; revision: number };

function OverridesSettings({ team, name, isAdmin, limits, periods, revision: saved }: Props) {
  const qc = useQueryClient();
  const [submitted, setSubmitted] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const base = overridesFormOf(limits, periods);
  const labels = Object.fromEntries([...limits.map((l) => [limitField(l.key), l.label]), ...periods.map((p) => [periodField(p.kind), retentionLabels[p.kind].label])]);
  const [form, setForm, revision] = useRevisionForm<OverridesForm>(base, saved, { labels, format: (_k, v) => overrideText(v) });
  const changes = overrideChanges(limits, periods, form);
  const invalid = overridesInvalid(limits, periods, form);
  const sooner = shorterPeriods(periods, form);
  const save = useMutation({
    mutationFn: async () => {
      let rev = saved;
      if (changes.limits.length) {
        const l = unwrap(await api.PUT("/v1/admin/teams/{team}/limits", { params: { path: { team }, header: ifMatch(rev) }, body: { items: changes.limits } }));
        qc.setQueryData(adminTeamLimitsQuery(team).queryKey, l);
        rev = l.revision;
      }
      if (changes.periods.length) {
        const r = unwrap(await api.PUT("/v1/admin/teams/{team}/retention", { params: { path: { team }, header: ifMatch(rev) }, body: { periods: changes.periods } }));
        qc.setQueryData(adminTeamRetentionQuery(team).queryKey, r);
      }
    },
    onSettled: () => {
      setConfirming(false);
      // The team's revision moved on: the other override, the team record and its pages load again.
      void qc.invalidateQueries({ queryKey: ["admin", "team", team] });
      void qc.invalidateQueries({ queryKey: ["team", team] });
    },
    onError: (err) => {
      if (isRevisionConflict(err)) void qc.invalidateQueries({ queryKey: ["admin", "team", team] });
    },
    onSuccess: () => {
      setSubmitted(false);
      toast.success("Team limits and retention saved", name);
    },
  });
  const refused = refusedOverride(save.error);
  const set = (next: OverridesForm) => {
    if (save.error) save.reset();
    setForm(next);
  };
  const dirty = changes.limits.length + changes.periods.length > 0 || JSON.stringify(form) !== JSON.stringify(base);
  return (
    <>
      <SettingsPage
        revision={revision}
        canEdit={isAdmin}
        readOnlyNote={adminOnly}
        dirty={dirty}
        saving={save.isPending}
        error={refused ? undefined : save.error}
        saveDisabled={submitted && invalid}
        message={invalid ? "Not saved: fix the highlighted values" : refused ? "Not saved: see the highlighted value" : undefined}
        onSave={() => {
          setSubmitted(true);
          if (invalid || changes.limits.length + changes.periods.length === 0) return;
          if (sooner.length > 0) setConfirming(true);
          else save.mutate();
        }}
        onDiscard={() => {
          setForm(base);
          setSubmitted(false);
        }}
      >
        <SettingsSection
          title="Rate limits"
          description={
            <>
              Requests per minute for {name}&apos;s service accounts, and the default for its gateways (each gateway can set its own). Above or below the platform default,
              up to its ceiling. Defaults and ceilings are on <TextLink render={<Link to="/admin/limits" />}>Limits</TextLink>.
            </>
          }
        >
          <LimitsTable
            name={name}
            items={limits}
            form={form}
            isAdmin={isAdmin}
            shown={submitted}
            refused={refused}
            onChange={(key, row) => set({ ...form, [limitField(key)]: row })}
          />
        </SettingsSection>
        <SettingsSection
          title="Retention"
          description={
            <>
              How long {name}&apos;s call records and recorded contents are kept. A call that also involves another team is kept for the shorter of the two periods. Platform
              periods are on <TextLink render={<Link to="/admin/retention" />}>Retention</TextLink>.
            </>
          }
        >
          <PeriodsTable
            name={name}
            items={periods}
            form={form}
            isAdmin={isAdmin}
            shown={submitted}
            refused={refused}
            onChange={(kind, row) => set({ ...form, [periodField(kind)]: row })}
          />
        </SettingsSection>
      </SettingsPage>
      <AlertDialog
        open={confirming}
        onOpenChange={(open) => {
          if (!open) setConfirming(false);
        }}
        title={`Delete ${name}'s records sooner?`}
        description="The next retention run permanently deletes what's older than the shorter periods. This can't be undone."
        confirmLabel="Save settings"
        busy={save.isPending}
        onConfirm={() => save.mutate()}
      >
        <ul>
          {sooner.map((p) => (
            <li key={p.kind}>{`${retentionLabels[p.kind].label}: ${daysText(p.effective)} → ${daysText(effectivePeriod(p, form[periodField(p.kind)] ?? blank))}`}</li>
          ))}
        </ul>
      </AlertDialog>
    </>
  );
}
