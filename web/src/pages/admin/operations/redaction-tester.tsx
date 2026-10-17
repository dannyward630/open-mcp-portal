/*
 * Payload capture's tester: what a sample would look like once recorded,
 * with the built-in rules and the patterns in the form (unsaved ones
 * included) or the saved ones. The sample is never stored, logged or
 * audited. The result is shown as text, with how many values each rule
 * replaced.
 */
import { useMutation } from "@tanstack/react-query";
import { useState } from "react";
import { api, ApiError, unwrap } from "@/api/client";
import { Alert, ErrorAlert } from "@/components/ui/alert/alert";
import { Badge } from "@/components/ui/badge/badge";
import { Button } from "@/components/ui/button/button";
import { Card } from "@/components/ui/card/card";
import { CodeBlock } from "@/components/ui/code-block/code-block";
import { Field, Form } from "@/components/ui/field/field";
import { Textarea } from "@/components/ui/input/input";
import { Table, Td, Tr } from "@/components/ui/table/table";
import { plural } from "@/lib/format";
import { patternReasons } from "./capture-labels";
import type { Pattern } from "./pattern-dialog";
import o from "./operations.module.css";

const maxSample = 65_536;

/** A rule's name for people: "pattern 2 (EMP-[0-9]{6})" for an admin pattern, "Sensitive member names", or the built-in rule's name. */
export function ruleLabel(rule: string, patterns: Pattern[]) {
  const m = /^pattern:(\d+)$/.exec(rule);
  if (m) {
    const i = Number(m[1]);
    return `Pattern ${i + 1}${patterns[i] ? ` (${patterns[i]!.pattern})` : ""}`;
  }
  return rule === "sensitive_keys" ? "Sensitive member names" : rule;
}

type Props = { patterns: Pattern[]; edited: boolean };

export function RedactionTester({ patterns, edited }: Props) {
  const [sample, setSample] = useState("");
  const test = useMutation({
    mutationFn: async (body: { sample: string; patterns?: Pattern[] }) => unwrap(await api.POST("/v1/admin/settings/capture/test", { body })),
  });
  // Which patterns the result used, kept with it.
  const [used, setUsed] = useState<Pattern[]>(patterns);
  const refused = test.error instanceof ApiError && test.error.code === "invalid_pattern" ? test.error : undefined;
  return (
    <Card title="Test redaction" description="Paste a sample (JSON, as a tool call's arguments or result, or plain text) to see what a recording would keep. The sample isn't stored or logged.">
      <Form
        noValidate
        className={o.tester}
        onSubmit={(e) => {
          e.preventDefault();
          if (!sample.trim()) return;
          setUsed(patterns);
          test.mutate(edited ? { sample, patterns } : { sample });
        }}
      >
        <Field label="Sample" description={edited ? "Tested with the patterns above, including your unsaved changes." : "Tested with the saved patterns."}>
          <Textarea rows={6} maxLength={maxSample} spellCheck={false} value={sample} onChange={(e) => setSample(e.target.value)} />
        </Field>
        <div className={o.actions}>
          <Button type="submit" variant="secondary" loading={test.isPending} disabled={!sample.trim()}>
            Test redaction
          </Button>
        </div>
      </Form>
      {refused ? (
        <Alert tone="danger" title="A pattern isn't valid">
          {`Pattern ${Number(refused.details?.index ?? 0) + 1}: ${patternReasons[String(refused.details?.reason ?? "")] ?? refused.message}`}
        </Alert>
      ) : (
        <ErrorAlert error={test.error} title="Couldn't test the sample" />
      )}
      {test.data && (
        <div className={o.tester} aria-live="polite">
          <p className={o.actions}>
            <Badge tone={test.data.redactions > 0 ? "info" : "neutral"}>{test.data.redactions > 0 ? `${plural(test.data.redactions, "value")} redacted` : "Nothing redacted"}</Badge>
            {test.data.truncated && <Badge tone="warning">Cut to 16 KiB</Badge>}
          </p>
          <CodeBlock code={test.data.redacted} filename="As recorded" wrap maxHeight="20rem" />
          {test.data.rules.length > 0 && (
            <Table caption="Rules that matched" columns={["Rule", { label: "Values", numeric: true }]} density="compact">
              {test.data.rules.map((r) => (
                <Tr key={r.rule}>
                  <Td>{ruleLabel(r.rule, used)}</Td>
                  <Td numeric>{r.count.toLocaleString()}</Td>
                </Tr>
              ))}
            </Table>
          )}
        </div>
      )}
    </Card>
  );
}
