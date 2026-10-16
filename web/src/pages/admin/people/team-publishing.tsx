/*
 * Admin team › Settings › Publishing: whether the team follows the platform's
 * default publishing policy or has its own (let a trusted team self-publish,
 * or hold one to approval). Not part of the page's save: a change asks
 * first and saves on its own, with If-Match on the team's revision.
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useRef, useState } from "react";
import { api, ifMatch, unwrap } from "@/api/client";
import { type PublishPolicy, policyHelp, policyLabels } from "@/components/servers/labels";
import { SettingsSection } from "@/components/templates/settings-page";
import { ErrorAlert } from "@/components/ui/alert/alert";
import { AlertDialog } from "@/components/ui/dialog/dialog";
import { Field } from "@/components/ui/field/field";
import { NativeSelect } from "@/components/ui/input/input";
import { SkeletonText } from "@/components/ui/skeleton/skeleton";
import { toast } from "@/components/ui/toast/toast";

type Choice = "default" | PublishPolicy;

export const teamPublishingKey = (team: string) => ["admin", "team", team, "publishing"];

export function TeamPublishingSection({ team, name, isAdmin }: { team: string; name: string; isAdmin: boolean }) {
  const qc = useQueryClient();
  const select = useRef<HTMLSelectElement>(null);
  const [pending, setPending] = useState<Choice | null>(null);
  const publishing = useQuery({
    queryKey: teamPublishingKey(team),
    queryFn: async () => unwrap(await api.GET("/v1/admin/teams/{team}/publishing", { params: { path: { team } } })),
  });
  const p = publishing.data;
  const update = useMutation({
    mutationFn: async (choice: Choice) =>
      unwrap(
        await api.PUT("/v1/admin/teams/{team}/publishing", {
          params: { path: { team }, header: ifMatch(p!.revision) },
          body: { override: choice === "default" ? null : choice },
        }),
      ),
    onSuccess: (r) => {
      setPending(null);
      qc.setQueryData(teamPublishingKey(team), r);
      // The team's revision moved on, and its servers have a new effective policy.
      void qc.invalidateQueries({ queryKey: ["admin"] });
      void qc.invalidateQueries({ queryKey: ["team", team] });
      toast.success("Publishing policy saved", r.override ? `${name}: ${policyLabels[r.override].toLowerCase()}.` : `${name} follows the platform default.`);
    },
  });
  const current: Choice = p?.override ?? "default";
  const effectiveAfter = pending === "default" ? p?.defaultPolicy : pending;
  return (
    <SettingsSection title="Publishing" description="How this team's servers reach the company catalog. Saved on its own, not with the form.">
      {publishing.isLoading ? (
        <SkeletonText lines={2} />
      ) : !p ? (
        <ErrorAlert error={publishing.error} title="Couldn't load the team's publishing policy" />
      ) : (
        <>
          <Field label="Publishing policy" description={`Now: ${policyLabels[p.effective].toLowerCase()}. ${policyHelp(p.effective)}`}>
            <NativeSelect
              ref={select}
              value={current}
              disabled={!isAdmin || update.isPending}
              onChange={(e) => {
                const next = e.target.value as Choice;
                if (next !== current) setPending(next);
              }}
            >
              <option value="default">Follow the platform default ({policyLabels[p.defaultPolicy].toLowerCase()})</option>
              <option value="approval_required">{policyLabels.approval_required}</option>
              <option value="self_publish">{policyLabels.self_publish}</option>
            </NativeSelect>
          </Field>
          <AlertDialog
            open={pending !== null}
            onOpenChange={(o) => {
              if (!o) {
                setPending(null);
                update.reset();
              }
            }}
            tone="primary"
            finalFocus={select}
            title={pending === "default" ? `Make ${name} follow the platform default?` : `Set ${name} to ${policyLabels[pending ?? "approval_required"].toLowerCase()}?`}
            description={effectiveAfter ? policyHelp(effectiveAfter) : ""}
            confirmLabel="Save policy"
            busy={update.isPending}
            error={update.error}
            onConfirm={() => pending && update.mutate(pending)}
          />
        </>
      )}
    </SettingsSection>
  );
}
