/*
 * Add or edit one allowlist entry (a staged change: the page's Save sends
 * the whole list). Kind, value, allow http and an optional note; typos are
 * caught here, broad networks and plain http are flagged as you type.
 */
import { useState } from "react";
import { FormDialog } from "@/components/form-dialog";
import { Alert } from "@/components/ui/alert/alert";
import { Checkbox } from "@/components/ui/checkbox/checkbox";
import { Field } from "@/components/ui/field/field";
import { Input, NativeSelect } from "@/components/ui/input/input";
import { entryWarnings, kindLabels, maxNote, type NetworkEntry, type NetworkKind, normalEntry, tidyValue, valueProblem } from "./entries";

type Props = {
  /** The entry being edited (undefined: a new one). */
  entry?: NetworkEntry;
  /** The other entries, to catch a duplicate. */
  others: NetworkEntry[];
  onSave: (e: NetworkEntry) => void;
  onClose: () => void;
};

export function EntryDialog({ entry, others, onSave, onClose }: Props) {
  const [kind, setKind] = useState<NetworkKind>(entry?.kind ?? "cidr");
  const [value, setValue] = useState(entry?.value ?? "");
  const [allowHttp, setAllowHttp] = useState(entry?.allowHttp ?? false);
  const [note, setNote] = useState(entry?.note ?? "");
  const [submitted, setSubmitted] = useState(false);
  const tidy = tidyValue(kind, value);
  const duplicate = others.some((o) => o.kind === kind && tidyValue(o.kind, o.value) === tidy);
  const problem = valueProblem(kind, value) ?? (duplicate ? "Another entry already lists this." : undefined);
  const noteProblem = note.trim().length > maxNote ? `Use at most ${maxNote} characters.` : undefined;
  const warnings = problem ? entryWarnings({ kind, value: "", allowHttp }) : entryWarnings({ kind, value: tidy, allowHttp });
  const submit = () => {
    setSubmitted(true);
    if (problem || noteProblem) return;
    onSave(normalEntry({ kind, value: tidy, allowHttp, note }));
  };
  return (
    <FormDialog
      title={entry ? "Edit entry" : "Add network or host"}
      description="MCP servers at these internal addresses can be registered. Loopback, link-local and cloud metadata addresses stay blocked whatever you add."
      onClose={onClose}
      onSubmit={submit}
      submitLabel={entry ? "Update entry" : "Add entry"}
      formProps={{ noValidate: true }}
    >
      <Field label="Kind">
        <NativeSelect value={kind} onChange={(e) => setKind(e.target.value as NetworkKind)}>
          {(Object.keys(kindLabels) as NetworkKind[]).map((k) => (
            <option key={k} value={k}>
              {kindLabels[k]}
            </option>
          ))}
        </NativeSelect>
      </Field>
      <Field
        label={kind === "cidr" ? "Network" : "Host name"}
        description={
          kind === "cidr"
            ? "An IPv4 or IPv6 prefix, such as 10.20.0.0/16 or fd00:1234::/32. Servers whose address is inside it, typed or resolved, are allowed."
            : "An exact name, such as mcp.corp.example.com, or *.corp.example.com for its subdomains (not corp.example.com itself). The internal addresses it resolves to are allowed."
        }
        error={submitted ? problem : undefined}
      >
        <Input autoComplete="off" spellCheck={false} maxLength={260} value={value} onChange={(e) => setValue(e.target.value)} />
      </Field>
      <Checkbox
        label="Allow plain http"
        description={kind === "cidr" ? "For URLs whose host is an address inside this network." : "For URLs whose host matches this name."}
        checked={allowHttp}
        onCheckedChange={setAllowHttp}
      />
      <Field label="Note" labelHint="Optional" description="Why this is allowed, or who owns it." error={submitted ? noteProblem : undefined}>
        <Input maxLength={maxNote} value={note} onChange={(e) => setNote(e.target.value)} />
      </Field>
      {warnings.length > 0 && (
        <Alert tone="warning" title="Check before you add it">
          {warnings.map((w) => (
            <p key={w}>{w}</p>
          ))}
        </Alert>
      )}
    </FormDialog>
  );
}
