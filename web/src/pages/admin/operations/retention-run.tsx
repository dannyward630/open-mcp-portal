/*
 * Admin › Retention's last run: when it ran, what it deleted per kind ("Nothing deleted" for a zero count, or for a
 * run that reported no kinds), and kinds that failed (retried at the next run).
 */
import type { Schemas } from "@/api/client";
import { Alert } from "@/components/ui/alert/alert";
import { Card } from "@/components/ui/card/card";
import { DescriptionList } from "@/components/ui/description-list/description-list";
import { Time } from "@/components/ui/time/time";
import { type RetentionKind, retentionKinds, retentionLabels } from "@/lib/retention";
import s from "../../shared.module.css";

const label = (kind: string) => retentionLabels[kind as RetentionKind]?.label ?? kind.replace(/_/g, " ");

export const deletedText = (n: number) => (n === 0 ? "Nothing deleted" : `${n.toLocaleString()} deleted`);

export function LastRun({ run }: { run: Schemas["RetentionRun"] | null }) {
  if (!run) {
    return (
      <Card title="Last run">
        <p className={s.note}>The retention job hasn&apos;t run yet. It runs every 15 minutes or so on one replica.</p>
      </Card>
    );
  }
  const kinds = [...retentionKinds.filter((k) => k in run.deleted || k in run.errors), ...Object.keys(run.deleted).filter((k) => !retentionKinds.includes(k as RetentionKind))];
  const errors = Object.entries(run.errors);
  return (
    <Card title="Last run" description="What the retention job deleted the last time it ran.">
      <DescriptionList
        dividers
        items={[
          { label: "Started", value: <Time value={run.startedAt} format="datetime" /> },
          { label: "Finished", value: <Time value={run.finishedAt} format="datetime" /> },
          ...kinds.map((k) => ({
            label: label(k),
            value: k in run.errors ? `Failed (${run.errors[k]})` : deletedText(run.deleted[k] ?? 0),
          })),
          ...(kinds.length === 0 ? [{ label: "Deleted", value: "Nothing deleted" }] : []),
        ]}
      />
      {errors.length > 0 && (
        <Alert tone="warning" title="Some kinds failed">
          {`${errors.map(([k]) => label(k)).join(", ")} couldn't be deleted. The next run tries again; the others were deleted as usual.`}
        </Alert>
      )}
    </Card>
  );
}
