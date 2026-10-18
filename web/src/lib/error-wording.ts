/*
 * Words for the error codes v0.2 adds (automation tokens, notifications), for ErrorAlert's `describe`. The API's own
 * messages are written for API clients; these say what the person can do. A code not listed here keeps the API's
 * message (describeError). Codes are in api/openapi.yaml.
 */
import { ApiError } from "../api/client";

type Wording = { title?: string; message: string };

const byCode: Record<string, Wording> = {
  // Automation tokens (docs/automation-tokens.md).
  token_not_allowed: { title: "Tokens can't do this", message: "Automation tokens are limited to a short list of operations. Do this in the web app, signed in as a person." },
  session_required: { title: "Sign in to do this", message: "Tokens are created and revoked in the web app, signed in as a person. A token can't create or revoke tokens." },
  too_many_tokens: { title: "Too many active tokens", message: "At most 20 tokens can be active at once. Revoke one you no longer use, then try again." },
  invalid_scopes: { message: "Choose at least one permission that belongs to this kind of token." },
  invalid_expiry: { message: "Choose an expiry from 1 to 365 days." },
  invalid_token: { title: "The token wasn't accepted", message: "It is unknown, expired or revoked, or it isn't an automation token. MCP keys don't work on the management API." },
  token_not_found: { title: "Token not found", message: "It may belong to another team, or it was already removed." },
  // Both credentials on one request.
  ambiguous_credentials: { title: "Two credentials on one request", message: "Send either a session or a token, not both. Remove the Authorization header or sign out of the browser session." },
  // Notifications.
  not_receivable: { title: "You can't receive that event", message: "Your role doesn't receive one of these events, so it can't be switched." },
  invalid_event: { message: "That isn't a known notification event." },
  invalid_events: { title: "Events this destination can't receive", message: "Remove the events that don't apply to a destination of this kind and try again." },
  invalid_name: { message: "Enter a name of 1 to 100 characters." },
  invalid_format: { message: "Choose JSON or Slack." },
  name_taken: { title: "Name already used", message: "Another destination uses this name. Choose a different one." },
  too_many_destinations: { title: "Too many destinations", message: "At most 10 destinations are allowed here. Delete one you no longer use." },
  not_signed: { title: "Slack destinations aren't signed", message: "Only JSON destinations have a signing secret to rotate." },
  team_archived: { title: "The team is archived", message: "Archived teams are read-only. Unarchive the team to change this." },
  email_not_configured: { title: "Email isn't set up", message: "The operator hasn't configured SMTP for this portal, so there is nothing to test." },
  notification_not_found: { title: "Notification not found", message: "It was deleted (notifications are kept for 90 days)." },
};

/** Why the URL of a webhook was refused (422 url_not_allowed, details.reason). */
export const urlReasonWording: Record<string, string> = {
  invalid: "That isn't a valid URL.",
  too_long: "The URL is too long (at most 1,000 characters).",
  userinfo: "Remove the user name and password from the URL.",
  fragment: "Remove the # part from the URL.",
  scheme: "Use an https URL (http works only for hosts in Admin › Internal networks that allow plain http).",
  private_address: "That address is on an internal network. Ask a platform admin to allow it under Admin › Internal networks.",
  blocked_address: "That address is never allowed (loopback, link-local or cloud metadata).",
};

/** The wording for an API error of one of v0.2's codes, for `ErrorAlert describe={describeV02}`; undefined for others. */
export function describeV02(err: unknown): Wording | undefined {
  if (!(err instanceof ApiError)) return undefined;
  if (err.code === "url_not_allowed") {
    const reason = typeof err.details?.reason === "string" ? err.details.reason : "";
    return { title: "That URL isn't allowed", message: urlReasonWording[reason] ?? "The portal can't send to that address." };
  }
  return byCode[err.code];
}

/**
 * The same wording for places that show only an error's message (a confirmation dialog): an ApiError whose message is
 * the person's wording, or the error itself when the code isn't one of v0.2's.
 */
export function friendly(err: unknown): unknown {
  if (!(err instanceof ApiError)) return err;
  const wording = describeV02(err);
  return wording ? new ApiError(err.status, err.code, wording.message, err.details, err.retryAfter) : err;
}
