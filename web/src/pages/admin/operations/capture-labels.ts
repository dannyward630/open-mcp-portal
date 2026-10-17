/* Payload capture's words: why the server refused a pattern (422 invalid_pattern), as the row it's about. */
import { ApiError } from "@/api/client";

export const patternReasons: Record<string, string> = {
  syntax: "Not a valid RE2 regular expression.",
  too_long: "Longer than 500 characters.",
  too_many: "More than 50 patterns.",
  matches_empty: "Matches empty text, so it would redact everywhere.",
};

export function refusedPattern(err: unknown): { index: number; message: string } | undefined {
  if (!(err instanceof ApiError) || err.code !== "invalid_pattern" || typeof err.details?.index !== "number") return undefined;
  const reason = typeof err.details.reason === "string" ? err.details.reason : "";
  return { index: err.details.index, message: patternReasons[reason] ?? err.message };
}
