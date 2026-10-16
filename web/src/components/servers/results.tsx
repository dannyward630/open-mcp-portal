/*
 * What a Test, a Refresh or the first discovery after Add found: a
 * connection result (ok, latency, what the server reported) or why it
 * failed, and what a refresh changed (added, changed, gone).
 */
import type { ReactNode } from "react";
import { ApiError, type Schemas } from "../../api/client";
import { plural } from "../../lib/format";
import { Alert, ErrorAlert } from "@/components/ui/alert/alert";
import s from "../../pages/shared.module.css";
import { countsText, failureDetail, failureText } from "./labels";
import m from "./servers.module.css";

type TestResult = Schemas["McpTestResult"];
type RefreshResult = Schemas["McpRefreshResult"];

/** "Service status 1.2.0 · MCP 2025-06-18". */
function reported(r: TestResult) {
  const name = [r.serverName, r.serverVersion].filter(Boolean).join(" ");
  return [name, r.protocolVersion ? `MCP ${r.protocolVersion}` : ""].filter(Boolean).join(" · ");
}

/** A failure: the class label once, and the server's message only when it says more. */
export function FailureLine({ result }: { result: Pick<TestResult, "errorClass" | "httpStatus" | "message"> }) {
  const detail = failureDetail(result);
  return (
    <>
      {failureText(result)}.{detail && <span className={m.message}> {detail}</span>}
    </>
  );
}

type AlertExtras = { title?: string; onDismiss?: () => void; children?: ReactNode };

/** A test's result: connected (with what the server lists) or why it failed. */
export function TestResultAlert({ result, title, onDismiss, children }: { result: TestResult } & AlertExtras) {
  if (result.ok) {
    const about = reported(result);
    return (
      <Alert tone="success" title={title ?? `Connected in ${result.latencyMs.toLocaleString()} ms`} onDismiss={onDismiss}>
        {about && <>The server says it's {about}. </>}
        {result.counts && <>It lists {countsText(result.counts).toLowerCase()}. </>}
        The test is kept in the health history. The capabilities weren't changed and no tool was called.
        {children}
      </Alert>
    );
  }
  return (
    <Alert tone="danger" title={title ?? "The test failed"} onDismiss={onDismiss}>
      <FailureLine result={result} />
      {children}
    </Alert>
  );
}

/** A refresh's 502 discovery_failed carries the test result in its details. */
export function discoveryFailure(err: unknown): TestResult | undefined {
  if (!(err instanceof ApiError) || err.code !== "discovery_failed" || !err.details) return undefined;
  const d = err.details as Partial<TestResult>;
  return typeof d.ok === "boolean" ? ({ latencyMs: 0, ...d } as TestResult) : undefined;
}

/** A failed refresh: why the server couldn't be read, or the plain error. */
export function RefreshError({ error }: { error: unknown }) {
  const failure = discoveryFailure(error);
  if (failure) return <TestResultAlert result={failure} title="The capabilities couldn't be read" />;
  return <ErrorAlert error={error} title="Couldn't refresh the capabilities" />;
}

/** "3 new, 1 changed, 2 gone", or "nothing changed". */
export function refreshChanges(r: Pick<RefreshResult, "added" | "changed" | "gone">) {
  const parts = [r.added && `${r.added} new`, r.changed && `${r.changed} changed`, r.gone && `${r.gone} gone`].filter(Boolean);
  return parts.length ? parts.join(", ") : "nothing changed";
}

/** What a refresh found. On a published server, changed capabilities wait for approval. */
export function RefreshSummary({ result, published, title, onDismiss }: { result: RefreshResult; published: boolean } & AlertExtras) {
  return (
    <Alert tone="success" title={title ?? `Read ${plural(result.listed, "capability", "capabilities")}: ${refreshChanges(result)}`} onDismiss={onDismiss}>
      {result.changed > 0 && published
        ? "Changed capabilities are hidden from the catalog until they're approved. Review them on the Capabilities tab."
        : result.gone > 0
          ? "Gone capabilities stay listed on the Capabilities tab, marked Gone."
          : "The Capabilities tab lists what the server offers."}
    </Alert>
  );
}

/** After Add: what the first discovery read, or why it couldn't (dismissable). */
export function FirstDiscovery({ created, onDismiss }: { created: Schemas["McpServerCreated"]; onDismiss: () => void }) {
  if (created.refresh) {
    return (
      <RefreshSummary
        result={created.refresh}
        published={false}
        title={`Added. The first read found ${plural(created.refresh.listed, "capability", "capabilities")}`}
        onDismiss={onDismiss}
      />
    );
  }
  if (created.refreshError) {
    return (
      <TestResultAlert result={created.refreshError} title="Added, but its capabilities couldn't be read" onDismiss={onDismiss}>
        <p className={s.note}>Check the URL and the header on the Settings tab, then press Refresh.</p>
      </TestResultAlert>
    );
  }
  return null;
}
