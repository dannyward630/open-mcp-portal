/* Error alerts that explain a rate limit instead of showing a generic failure. */
import { ApiError } from "../api/client";
import { describeV02 } from "../lib/error-wording";
import { Alert, ErrorAlert, type ErrorAlertProps } from "@/components/ui/alert/alert";

/** A title and message for a 429 rate_limited error; undefined for other errors. */
export function rateLimitError(err: unknown): { title: string; message: string } | undefined {
  if (!(err instanceof ApiError) || err.status !== 429) return undefined;
  const wait = err.retryAfter && err.retryAfter <= 120 ? ` You can try again in ${err.retryAfter} s.` : "";
  return { title: "Too many requests", message: err.message + wait };
}

/**
 * Like ErrorAlert, but a rate limit (429) is shown as a warning with a title
 * and when to try again.
 */
export function ApiErrorAlert({ error, title, ...props }: ErrorAlertProps) {
  const limit = rateLimitError(error);
  if (limit) {
    return (
      <Alert tone="warning" title={limit.title} {...props}>
        {limit.message}
      </Alert>
    );
  }
  return <ErrorAlert error={error} title={title} {...props} />;
}

/**
 * An error alert with the words of v0.2's error codes (lib/error-wording.ts): the code's own title and message when it
 * has them (a 403 token_not_allowed is "Tokens can't do this", not "You don't have access"), else `title`.
 */
export function V02ErrorAlert({ error, title, ...props }: ErrorAlertProps) {
  return <ApiErrorAlert error={error} title={describeV02(error)?.title ?? title} describe={describeV02} {...props} />;
}
