/*
 * Create an automation token: a name (where it runs), what it may do (read, or read and write: write includes read),
 * and how many days it lasts (tokens always expire: 90 by default, at most 365). The dialog lists what no token of
 * this kind can ever do. The secret is shown once, right after, with a copy button and a usage hint that never holds
 * the secret itself; it's gone when the dialog closes.
 */
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useId, useState } from "react";
import type { Schemas } from "../../api/client";
import { V02ErrorAlert } from "../errors";
import { Alert } from "@/components/ui/alert/alert";
import { Button } from "@/components/ui/button/button";
import { CopyField } from "@/components/ui/copy-field/copy-field";
import { Dialog, DialogClose } from "@/components/ui/dialog/dialog";
import { Field, Form } from "@/components/ui/field/field";
import { Input } from "@/components/ui/input/input";
import { Stack } from "@/components/ui/layout/layout";
import { NumberInput } from "@/components/ui/number-input/number-input";
import { RadioGroup } from "@/components/ui/radio-group/radio-group";
import { type TokenScope, type TokenScopeName, createToken, tokensKey } from "./api";
import { cannotList, defaultExpiryDays, maxExpiryDays, scopeChoices, usageSnippet } from "./labels";

const daysProblem = (text: string) => {
  const n = Number(text);
  return text && Number.isInteger(n) && n >= 1 && n <= maxExpiryDays ? undefined : `Enter a whole number of days from 1 to ${maxExpiryDays}.`;
};

/** What no token of this kind can do: always visible, so nobody hands over more than they think. */
export function CannotDo({ scope }: { scope: TokenScope }) {
  return (
    <Alert tone="info" title="What this token can never do, whatever you choose">
      <ul>
        {cannotList(scope).map((line) => (
          <li key={line}>{line}</li>
        ))}
      </ul>
    </Alert>
  );
}

export function CreateTokenDialog({ scope, onClose }: { scope: TokenScope; onClose: () => void }) {
  const qc = useQueryClient();
  const formId = useId();
  const choices = scopeChoices(scope);
  const [name, setName] = useState("");
  const [chosen, setChosen] = useState<TokenScopeName>(choices[0]!.value);
  const [days, setDays] = useState(String(defaultExpiryDays));
  const [submitted, setSubmitted] = useState(false);
  const create = useMutation({
    mutationFn: () => createToken(scope, { name: name.trim(), scopes: [chosen], expiresInDays: Number(days) }),
    onSuccess: () => void qc.invalidateQueries({ queryKey: tokensKey(scope) }),
  });

  // One Dialog in both states, so it stays open (and keeps focus) when the secret appears.
  if (create.data) {
    return (
      <Dialog open onOpenChange={(o) => !o && onClose()} title="Token created" size="lg" footer={<Button onClick={onClose}>Done</Button>}>
        <SecretView created={create.data} scope={scope} />
      </Dialog>
    );
  }
  const nameError = submitted && !name.trim() ? "Enter a name." : undefined;
  const daysError = submitted ? daysProblem(days) : undefined;
  return (
    <Dialog
      open
      onOpenChange={(o) => !o && onClose()}
      title={scope.kind === "team" ? "Create a publisher token" : "Create a platform-admin token"}
      description={
        scope.kind === "team"
          ? "For a pipeline that registers, refreshes and publishes this team's servers. The token belongs to the team and keeps working if you leave it. The secret is shown once."
          : "For scripts that read the platform's state or provision teams and gateways. It stops working if you stop being a platform admin. The secret is shown once."
      }
      size="lg"
      footer={
        <>
          <DialogClose>Cancel</DialogClose>
          <Button type="submit" form={formId} loading={create.isPending}>
            Create token
          </Button>
        </>
      }
    >
      <Form
        id={formId}
        noValidate
        onSubmit={(e) => {
          e.preventDefault();
          setSubmitted(true);
          if (name.trim() && !daysProblem(days)) create.mutate();
        }}
      >
        <Field label="Name" description="Where it runs, for example “GitHub Actions release workflow”." error={nameError}>
          <Input maxLength={100} value={name} onChange={(e) => setName(e.target.value)} />
        </Field>
        <RadioGroup<TokenScopeName>
          legend="What it may do"
          value={chosen}
          onValueChange={setChosen}
          options={choices.map((c) => ({
            value: c.value,
            label: (
              <>
                {c.label} <code>{c.value}</code>
              </>
            ),
            description: c.can,
          }))}
        />
        <Field label="Expires after (days)" description={`Tokens always expire: from 1 to ${maxExpiryDays} days. Make a new one before it does.`} error={daysError}>
          <NumberInput value={days} onValueChange={setDays} maximumFractionDigits={0} unit="days" />
        </Field>
        <CannotDo scope={scope} />
        <V02ErrorAlert error={create.error} title="Couldn't create the token" />
      </Form>
    </Dialog>
  );
}

/** The secret once, and how to use it. */
function SecretView({ created, scope }: { created: Schemas["AutomationTokenCreated"]; scope: TokenScope }) {
  return (
    <Stack gap={5}>
      <Alert tone="warning" title="Copy this token now.">
        It won&apos;t be shown again. Store it in the secret store of the system that uses it (your CI&apos;s secrets, a vault). If you lose it, revoke it and create a new one.
      </Alert>
      <CopyField label="Token" name="automation token" value={created.secret} description={`Starts with ${created.token.prefix}. Send it as “Authorization: Bearer …” to the management API (/v1/…), never to an MCP gateway.`} />
      <CopyField
        label="Using it in CI"
        name="usage example"
        multiline
        value={usageSnippet(scope, globalThis.location?.origin ?? "https://portal.example.org")}
        description="Store the token as a secret (here OMP_TOKEN) and read it from the environment. This example never contains the token."
      />
    </Stack>
  );
}
