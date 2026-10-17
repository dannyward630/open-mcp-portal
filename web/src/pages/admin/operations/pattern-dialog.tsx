/*
 * Add or edit one redaction pattern (a staged change: Payload capture's Save
 * sends the whole list). An RE2 regular expression and an optional note;
 * length and duplicates are checked here. The syntax is checked here too
 * where the browser's RegExp reads the pattern as RE2 does (checkPattern);
 * the server's RE2 rules decide on Save (and in the tester).
 */
import { useState } from "react";
import type { Schemas } from "@/api/client";
import { FormDialog } from "@/components/form-dialog";
import { Field } from "@/components/ui/field/field";
import { Input } from "@/components/ui/input/input";
import { patternReasons } from "./capture-labels";

export type Pattern = Schemas["RedactionPattern"];

export const maxPatterns = 50;
export const maxPatternLength = 500;
export const maxPatternNote = 200;

/*
 * RE2 (Go regexp) and JavaScript's RegExp (without the u flag) agree on the common syntax: literals, classes,
 * escapes such as \d \w \s \b, groups, (?:…), (?<name>…), alternation and quantifiers. Where they don't:
 *  - RE2 only: inline flags other than a leading (?i), (?s) or (?m) ((?U), (?i:…), flags later on), (?P<name>…),
 *    \A \z \Q…\E \C, \pL and \p{Greek}, POSIX classes [[:alpha:]], and a class starting with "]". JavaScript would
 *    read these differently (or as literals), so such a pattern isn't checked here.
 *  - JavaScript only: lookahead, lookbehind and backreferences. RE2 refuses them, so they're reported.
 */
type Syntax = { kind: "re2_only" } | { kind: "js_only" } | { kind: "common"; body: string; flags: string };

function classify(pattern: string): Syntax {
  const lead = /^\(\?([ims]+)\)/.exec(pattern);
  const body = lead ? pattern.slice(lead[0].length) : pattern;
  for (let i = 0; i < body.length; i++) {
    const c = body[i];
    if (c === "\\") {
      const n = body[i + 1] ?? "";
      if ("AzQECpP".includes(n) && n) return { kind: "re2_only" };
      if ((/[1-9]/.test(n) && !/[0-9]/.test(body[i + 2] ?? "")) || (n === "k" && body[i + 2] === "<")) return { kind: "js_only" };
      i++;
      continue;
    }
    if (c === "[") {
      let j = i + 1;
      if (body[j] === "^") j++;
      if (body[j] === "]" || (body[j] === "[" && body[j + 1] === ":")) return { kind: "re2_only" };
      for (; j < body.length && body[j] !== "]"; j++) {
        if (body[j] === "\\") {
          if ("pPQE".includes(body[j + 1] ?? "x")) return { kind: "re2_only" };
          j++;
        } else if (body[j] === "[" && body[j + 1] === ":") return { kind: "re2_only" };
      }
      i = j;
      continue;
    }
    if (c === "(" && body[i + 1] === "?") {
      const rest = body.slice(i + 2);
      if (/^<?[=!]/.test(rest)) return { kind: "js_only" };
      if (rest.startsWith("P<") || /^[a-zA-Z-]+[:)]/.test(rest)) return { kind: "re2_only" };
    }
  }
  return { kind: "common", body, flags: [...new Set(lead?.[1] ?? "")].join("") };
}

export type PatternCheck =
  /** Checked here: problem is what's wrong, if anything. */
  | { checked: true; problem?: string }
  /** Uses RE2-only syntax: only the server can check it. */
  | { checked: false };

/** The syntax and empty-match check, in the browser where RegExp and RE2 read the pattern alike. */
export function checkPattern(pattern: string): PatternCheck {
  const syntax = classify(pattern);
  if (syntax.kind === "re2_only") return { checked: false };
  if (syntax.kind === "js_only") return { checked: true, problem: "RE2 doesn't support lookahead, lookbehind or backreferences." };
  let re: RegExp;
  try {
    re = new RegExp(syntax.body, syntax.flags);
  } catch (err) {
    // "Invalid regular expression: /(/: Unterminated group" → "unterminated group".
    const reason = err instanceof Error ? (err.message.split(": ").pop() ?? "").trim() : "";
    return { checked: true, problem: reason ? `Not a valid regular expression: ${reason.charAt(0).toLowerCase()}${reason.slice(1)}.` : patternReasons.syntax };
  }
  return re.test("") ? { checked: true, problem: patternReasons.matches_empty } : { checked: true };
}

/** What's wrong with a pattern before the server sees it. */
export function patternProblem(pattern: string, others: Pattern[]): string | undefined {
  if (!pattern.trim()) return "Enter a regular expression.";
  if (pattern.length > maxPatternLength) return `Use at most ${maxPatternLength} characters.`;
  if (others.some((o) => o.pattern === pattern)) return "Another pattern is the same.";
  const check = checkPattern(pattern);
  return check.checked ? check.problem : undefined;
}

const syntaxHelp = "RE2 syntax (Go), case-sensitive unless it starts with (?i); it must not match empty text. For example EMP-[0-9]{6} for employee numbers.";

type Props = { pattern?: Pattern; others: Pattern[]; onSave: (p: Pattern) => void; onClose: () => void };

export function PatternDialog({ pattern, others, onSave, onClose }: Props) {
  const [value, setValue] = useState(pattern?.pattern ?? "");
  const [note, setNote] = useState(pattern?.note ?? "");
  const [submitted, setSubmitted] = useState(false);
  const problem = patternProblem(value, others);
  const unchecked = value.trim() !== "" && !checkPattern(value).checked;
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
        description={
          unchecked
            ? `${syntaxHelp} This one uses syntax only RE2 has, so it can't be checked here: the server checks it when you save (or test it below).`
            : `${syntaxHelp} Common mistakes are caught here; the server's RE2 rules decide when you save.`
        }
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
