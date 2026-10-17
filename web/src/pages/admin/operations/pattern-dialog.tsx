/*
 * Add or edit one redaction pattern (a staged change: Payload capture's Save
 * sends the whole list). An RE2 regular expression and an optional note;
 * length and duplicates are checked here, syntax by the server on Save (and
 * by the tester).
 */
import { useState } from "react";
import type { Schemas } from "@/api/client";
import { FormDialog } from "@/components/form-dialog";
import { Field } from "@/components/ui/field/field";
import { Input } from "@/components/ui/input/input";

export type Pattern = Schemas["RedactionPattern"];

export const maxPatterns = 50;
export const maxPatternLength = 500;
export const maxPatternNote = 200;

/** What's wrong with a pattern before the server sees it. */
export function patternProblem(pattern: string, others: Pattern[]): string | undefined {
  if (!pattern.trim()) return "Enter a regular expression.";
  if (pattern.length > maxPatternLength) return `Use at most ${maxPatternLength} characters.`;
  if (others.some((o) => o.pattern === pattern)) return "Another pattern is the same.";
  return undefined;
}

type Props = { pattern?: Pattern; others: Pattern[]; onSave: (p: Pattern) => void; onClose: () => void };

export function PatternDialog({ pattern, others, onSave, onClose }: Props) {
  const [value, setValue] = useState(pattern?.pattern ?? "");
  const [note, setNote] = useState(pattern?.note ?? "");
  const [submitted, setSubmitted] = useState(false);
  const problem = patternProblem(value, others);
  return (
    <FormDialog
      title={pattern ? "Edit pattern" : "Add pattern"}
      description="Every match in a recorded string becomes [REDACTED], from the next recorded call on. Recordings already stored aren't changed."
      onClose={onClose}
      onSubmit={() => {
        setSubmitted(true);
        if (problem) return;
        onSave(note.trim() ? { pattern: value, note: note.trim() } : { pattern: value });
      }}
      submitLabel={pattern ? "Update pattern" : "Add pattern"}
      formProps={{ noValidate: true }}
    >
      <Field
        label="Regular expression"
        description="RE2 syntax (Go), case-sensitive unless it starts with (?i); it must not match empty text. For example EMP-[0-9]{6} for employee numbers."
        error={submitted ? problem : undefined}
      >
        <Input autoComplete="off" spellCheck={false} maxLength={maxPatternLength} value={value} onChange={(e) => setValue(e.target.value)} />
      </Field>
      <Field label="Note" labelHint="Optional" description="What it catches, for the next admin.">
        <Input maxLength={maxPatternNote} value={note} onChange={(e) => setNote(e.target.value)} />
      </Field>
    </FormDialog>
  );
}
