/*
 * Trusted clients (decisions 12 and 24): the words, and the checks that
 * catch a mistyped client ID before Save (the server has the last word,
 * 422 invalid_client_id). Only Client ID Metadata Document clients can be
 * trusted: their client_id is an https address with a path. A client that
 * registered itself (dynamic registration) gets a new ID each time, so it
 * can never be on the list.
 */
import type { Schemas } from "@/api/client";

export type TrustedClient = Schemas["TrustedClient"];
export type TrustedClients = Schemas["TrustedClients"];

export const trustedClientsKey = ["admin", "trusted-clients"];

/** The API's limits. */
export const maxClients = 50;
export const maxClientId = 500;
export const maxLabel = 100;

/** A client as the API returns it: trimmed, without an empty label, in the server's field order. */
export function normalClient(c: TrustedClient): TrustedClient {
  const label = c.label?.trim();
  return { clientId: c.clientId.trim(), ...(label ? { label } : {}) };
}

/** A list's identity for "has it changed". */
export const clientsKey = (clients: TrustedClient[]) => JSON.stringify(clients.map((c) => [c.clientId.trim(), c.label?.trim() ?? ""]));

/** What's wrong with a client ID, before it reaches the server. */
export function clientIdProblem(value: string): string | undefined {
  const v = value.trim();
  if (!v) return "Enter the client's ID: the https address of its metadata document.";
  if (v.length > maxClientId) return `Use at most ${maxClientId} characters.`;
  if (!/^[a-z][a-z0-9+.-]*:/i.test(v)) {
    return "This isn't an https address. Clients that registered themselves can't be trusted (their IDs change); enter a metadata document's address, such as https://claude.ai/oauth/claude-code-client-metadata.";
  }
  let url: URL;
  try {
    url = new URL(v);
  } catch {
    return "Enter a full https address, such as https://claude.ai/oauth/claude-code-client-metadata.";
  }
  if (url.protocol !== "https:") return "Only https addresses can be trusted.";
  if (url.username || url.password) return "Remove the user name and password from the address.";
  if (url.hash || v.includes("#")) return "Remove the #fragment: a client ID has none.";
  if (url.pathname === "/" || url.pathname === "") return "Add the document's path: a client ID is its metadata document's address, such as https://vscode.dev/oauth/client-metadata.json.";
  return undefined;
}

/** The host that vouches for a client ("claude.ai"), shown under its ID. */
export function clientHost(value: string): string | undefined {
  try {
    return new URL(value).host;
  } catch {
    return undefined;
  }
}

/** Why the server refused the list (422 invalid_client_id: details.index and details.reason). */
export const refusedClientReasons: Record<string, string> = {
  not_metadata_url: "The portal doesn't accept this ID: it must be an https address with a path (a Client ID Metadata Document).",
  duplicate: "This client is listed twice.",
  too_many: `At most ${maxClients} trusted clients.`,
};

export const refusedClient = (reason: unknown) => (typeof reason === "string" && refusedClientReasons[reason]) || refusedClientReasons.not_metadata_url!;
