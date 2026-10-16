/*
 * The URL checker: would the gateway reach this MCP server URL now, and why.
 * The server resolves the host (briefly) without connecting and judges every
 * address against the saved list, as a connection would.
 */
import { useMutation } from "@tanstack/react-query";
import { useState } from "react";
import { api, unwrap } from "@/api/client";
import { Alert, ErrorAlert } from "@/components/ui/alert/alert";
import { Badge } from "@/components/ui/badge/badge";
import { Button } from "@/components/ui/button/button";
import { Card } from "@/components/ui/card/card";
import { Field, Form } from "@/components/ui/field/field";
import { Input } from "@/components/ui/input/input";
import { Table, Td, Tr } from "@/components/ui/table/table";
import s from "../../shared.module.css";
import { checkReasonLabels, kindLabels, type NetworkCheck } from "./entries";
import n from "./networks.module.css";

export function UrlChecker() {
  const [url, setUrl] = useState("");
  const check = useMutation({
    mutationFn: async (u: string) => unwrap(await api.POST("/v1/admin/settings/network/check", { body: { url: u } })),
  });
  return (
    <Card title="Check a URL" description="Whether the gateway would connect to an MCP server URL now, with the saved list. The host is looked up but never contacted.">
      <Form
        noValidate
        className={n.checkForm}
        onSubmit={(e) => {
          e.preventDefault();
          if (url.trim()) check.mutate(url.trim());
        }}
      >
        <Field label="MCP server URL" className={n.checkField}>
          <Input
            type="url"
            inputMode="url"
            autoComplete="off"
            spellCheck={false}
            placeholder="https://mcp.corp.example.com/mcp"
            value={url}
            onChange={(e) => setUrl(e.target.value)}
          />
        </Field>
        <Button type="submit" variant="secondary" loading={check.isPending} disabled={!url.trim()}>
          Check URL
        </Button>
      </Form>
      <ErrorAlert error={check.error} title="Couldn't check the URL" />
      {check.data && <CheckResult result={check.data} />}
    </Card>
  );
}

function CheckResult({ result }: { result: NetworkCheck }) {
  const addresses = result.resolvedAddresses ?? [];
  return (
    <div className={n.result} aria-live="polite">
      <Alert tone={result.allowed ? "success" : "danger"} title={result.allowed ? "Allowed" : `Not allowed: ${checkReasonLabels[result.reason]?.toLowerCase() ?? result.reason}`}>
        <p>{result.message}</p>
        {result.matchedEntry && (
          <p>
            Matched {kindLabels[result.matchedEntry.kind].toLowerCase()} <span className={s.mono}>{result.matchedEntry.value}</span>
            {result.matchedEntry.note ? ` (${result.matchedEntry.note})` : ""}.
          </p>
        )}
      </Alert>
      {addresses.length > 0 && (
        <Table caption="Addresses checked" columns={["Address", "Verdict"]} density="compact">
          {addresses.map((a) => (
            <Tr key={a.address}>
              <Td>
                <span className={s.mono}>{a.address}</span>
              </Td>
              <Td>
                <Badge tone={a.allowed ? "success" : "danger"}>{checkReasonLabels[a.reason] ?? a.reason}</Badge>
              </Td>
            </Tr>
          ))}
        </Table>
      )}
    </div>
  );
}
