/*
 * The words for the portal's refusals about an MCP server's sign-in
 * (docs/phase7-upstream-oauth.md), for ErrorAlert's `describe`: what went
 * wrong and what to do, instead of the server's terse message.
 */
import { ApiError } from "../../api/client";

type Described = { title?: string; message: string };

/** details.problems of 422 oauth_discovery_failed, as the server worded them. */
function problemsOf(details: Record<string, unknown> | undefined): string {
  const list = Array.isArray(details?.problems) ? details.problems : [];
  const words = list.map((p) => (p && typeof p === "object" && "message" in p && typeof p.message === "string" ? p.message : "")).filter(Boolean);
  return words.length ? ` ${words.join(" ")}` : "";
}

const conflictReasons: Record<string, string> = {
  static_authorization: "OAuth sign-in sends each person's token in the Authorization header, so the server can't also have a static Authorization header. Remove the header, or use another header name.",
  requirement: "OAuth sign-in replaces User authorization: each call carries the person's own token for this server. Remove the server's requirement first.",
};

/** The error's title and message for the sign-in error codes; undefined for any other error. */
export function describeAuthError(err: unknown): Described | undefined {
  if (!(err instanceof ApiError)) return undefined;
  const reason = typeof err.details?.reason === "string" ? err.details.reason : "";
  switch (err.code) {
    case "oauth_discovery_failed":
      return { title: "The server's sign-in couldn't be discovered", message: `Nothing was saved.${problemsOf(err.details)} Press Detect sign-in to see what the server answers.` };
    case "oauth_registration_failed":
      return {
        title: "The authorization server refused to register the portal",
        message: "Nothing was saved. Try again, or create an OAuth app at the authorization server and enter its client ID and secret instead.",
      };
    case "auth_conflict":
      return { title: "OAuth sign-in can't be combined with this", message: conflictReasons[reason] ?? "OAuth sign-in excludes a static Authorization header and a User authorization requirement." };
    case "registration_unavailable":
      return { title: "That registration isn't offered", message: "The authorization server no longer offers this kind of registration. Press Detect sign-in again and choose another." };
    case "client_required":
      return { title: "Enter the client", message: "Enter the client ID and secret of the OAuth app you created at the authorization server (no secret only for a public client)." };
    case "provider_mismatch":
      return { title: "The platform provider doesn't match", message: "The linked platform provider was turned off, or its endpoints no longer match this server's authorization server. Choose another registration." };
    case "setup_reconsent_required":
      return {
        title: "Nobody's sign-in can read the capabilities",
        message: "The sign-in used to read them lapsed, was disconnected, or its person can no longer edit the server. Authorize as yourself to read them again.",
      };
    case "upstream_oauth":
      return { message: "This server uses OAuth sign-in: every call carries the person's own token for it, and its scopes come from the server, so a capability can't ask for another account." };
    case "not_oauth":
      return { title: "Not an OAuth server any more", message: "This server no longer uses OAuth sign-in, so there's nothing to sign in to. Reload the page." };
    default:
      return undefined;
  }
}
