/*
 * A recorded call's contents (payload capture): nothing is fetched until the
 * person asks, after a notice that viewing is recorded in the audit log
 * (every successful read is audited, so the request is a mutation: it never
 * reruns by itself on focus or remount). The arguments and result are shown
 * as plain text, never as HTML, with what was cut and how many values were
 * redacted.
 */
import { useMutation } from "@tanstack/react-query";
import { Eye, FileWarning } from "lucide-react";
import type { ReactNode } from "react";
import { api, type Schemas, unwrap } from "../../api/client";
import { plural } from "../../lib/format";
import { Alert, ErrorAlert } from "@/components/ui/alert/alert";
import { Badge } from "@/components/ui/badge/badge";
import { Button } from "@/components/ui/button/button";
import { Card } from "@/components/ui/card/card";
import { CodeBlock } from "@/components/ui/code-block/code-block";
import { DescriptionList } from "@/components/ui/description-list/description-list";
import { Time } from "@/components/ui/time/time";
import s from "../../pages/shared.module.css";
import { isNotFound } from "../not-found";
import g from "./call.module.css";

type Payload = Schemas["CallPayload"];
type Part = Schemas["CallPayloadPart"];

export type PayloadScope = { kind: "team"; team: string } | { kind: "admin" };

/** The viewing notice, the button's name and what the audit log will show. */
export const viewPayloadLabel = "View contents";
export const viewPayloadNotice = "Viewing the contents is recorded in the audit log, with your name and the time. The team that recorded them sees it in its own log.";

const byLabels: Record<"gateway" | "server", string> = { gateway: "the gateway", server: "the server" };

/** Pretty-printed JSON when it parses (a cut part doesn't); the text as recorded otherwise. */
export function partText(p: Part): string {
  if (p.truncated) return p.content;
  try {
    return JSON.stringify(JSON.parse(p.content), null, 2);
  } catch {
    return p.content;
  }
}

function PartBlock({ title, part }: { title: string; part: Part | null }) {
  if (!part) {
    return (
      <div className={g.stack}>
        <h3 className={g.partTitle}>{title}</h3>
        <p className={s.note}>Nothing was received (the call timed out, the server was unreachable, or the answer was too large).</p>
      </div>
    );
  }
  return (
    <div className={g.stack}>
      <h3 className={g.partTitle}>
        {title}
        <span className={s.badges}>
          {part.truncated && (
            <Badge size="sm" tone="warning" title="Cut to 16 KiB, so it's no longer valid JSON">
              Cut to 16 KiB
            </Badge>
          )}
          {part.redactions > 0 && (
            <Badge size="sm" tone="info">
              {plural(part.redactions, "value")} redacted
            </Badge>
          )}
        </span>
      </h3>
      {/* CodeBlock shows the text as text: a recorded "<script>" is just characters. */}
      <CodeBlock code={partText(part)} language="json" filename={`${title} (${part.bytes.toLocaleString()} bytes)`} wrap maxHeight="24rem" />
    </div>
  );
}

function PayloadView({ payload }: { payload: Payload }) {
  return (
    <div className={g.stack}>
      <DescriptionList
        dividers
        items={[
          { label: "Recorded by", value: payload.capturedBy.map((b) => byLabels[b]).join(" and ") },
          { label: "Recorded", value: <Time value={payload.capturedAt} format="datetime" /> },
          { label: "Deleted after", value: <Time value={payload.expiresAt} format="date" /> },
          { label: "Redacted", value: payload.redactions > 0 ? plural(payload.redactions, "value") : "Nothing" },
        ]}
      />
      <PartBlock title="Arguments" part={payload.arguments} />
      <PartBlock title="Result" part={payload.result} />
    </div>
  );
}

export function CallPayloadCard({ scope, callId }: { scope: PayloadScope; callId: number }) {
  const load = useMutation({
    mutationFn: async () =>
      scope.kind === "team"
        ? unwrap(await api.GET("/v1/teams/{team}/calls/{callId}/payload", { params: { path: { team: scope.team, callId } } }))
        : unwrap(await api.GET("/v1/admin/calls/{callId}/payload", { params: { path: { callId } } })),
  });
  return (
    <Card title="Contents" description="What the client sent and what it got back, as recorded: secret-looking values were replaced with [REDACTED] and each part cut to 16 KiB.">
      {load.data ? (
        <PayloadView payload={load.data} />
      ) : isNotFound(load.error) ? (
        <Alert tone="warning" title="The contents are gone">
          They were deleted after their retention period, or they were recorded under another team&apos;s setting.
        </Alert>
      ) : (
        <div className={g.stack}>
          <Alert tone="info" title="Viewing is recorded">
            {viewPayloadNotice}
          </Alert>
          <ErrorAlert error={load.error} title="Couldn't load the contents" />
          <div>
            <Button variant="secondary" loading={load.isPending} onClick={() => load.mutate()}>
              <Eye aria-hidden /> {viewPayloadLabel}
            </Button>
          </div>
        </div>
      )}
    </Card>
  );
}

/** A recorded call whose contents the viewer can't open here (platform staff on a team's page). */
export function PayloadElsewhere({ children }: { children: ReactNode }) {
  return (
    <Card title="Contents">
      <p className={`${s.note} ${g.iconLine}`}>
        <FileWarning aria-hidden /> {children}
      </p>
    </Card>
  );
}
