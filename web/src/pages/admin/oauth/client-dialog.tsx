/*
 * Add or edit one trusted client (a staged change: the page's Save sends the
 * whole list). Its client ID (a metadata document's https address) and an
 * optional label; typos and duplicates are caught here.
 */
import { useState } from "react";
import { FormDialog } from "@/components/form-dialog";
import { Field } from "@/components/ui/field/field";
import { Input } from "@/components/ui/input/input";
import { clientIdProblem, maxClientId, maxLabel, normalClient, type TrustedClient } from "./clients";

type Props = {
  /** The client being edited (undefined: a new one). */
  client?: TrustedClient;
  /** The other clients, to catch a duplicate. */
  others: TrustedClient[];
  onSave: (c: TrustedClient) => void;
  onClose: () => void;
};

export function ClientDialog({ client, others, onSave, onClose }: Props) {
  const [clientId, setClientId] = useState(client?.clientId ?? "");
  const [label, setLabel] = useState(client?.label ?? "");
  const [submitted, setSubmitted] = useState(false);
  const duplicate = others.some((o) => o.clientId.trim() === clientId.trim());
  const problem = clientIdProblem(clientId) ?? (duplicate ? "This client is already on the list." : undefined);
  const labelProblem = label.trim().length > maxLabel ? `Use at most ${maxLabel} characters.` : undefined;
  const submit = () => {
    setSubmitted(true);
    if (problem || labelProblem) return;
    onSave(normalClient({ clientId, label }));
  };
  return (
    <FormDialog
      title={client ? "Edit trusted client" : "Add trusted client"}
      description="People connecting this client skip the consent screen. They still sign in, and it still gets only their access."
      onClose={onClose}
      onSubmit={submit}
      submitLabel={client ? "Update client" : "Add client"}
      formProps={{ noValidate: true }}
    >
      <Field
        label="Client ID"
        description="The https address of the client's metadata document (its OAuth client_id), such as https://vscode.dev/oauth/client-metadata.json."
        error={submitted ? problem : undefined}
      >
        <Input autoComplete="off" spellCheck={false} inputMode="url" maxLength={maxClientId} value={clientId} onChange={(e) => setClientId(e.target.value)} />
      </Field>
      <Field label="Label" labelHint="Optional" description="A name for this list, such as “Claude Code”." error={submitted ? labelProblem : undefined}>
        <Input maxLength={maxLabel} value={label} onChange={(e) => setLabel(e.target.value)} />
      </Field>
    </FormDialog>
  );
}
