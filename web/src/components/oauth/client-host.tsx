/*
 * Where an OAuth client comes from, said carefully: a metadata-document
 * client's host vouches for it (a fact); a client that registered itself
 * only claims its name and home page (anyone can register anything), so it
 * reads as unverified.
 */
import type { Schemas } from "../../api/client";
import s from "../../pages/shared.module.css";

type Client = Schemas["OAuthClientInfo"];

/** The client's logo, only from an https address (anything else could track or mislead). */
export function safeLogo(client: Pick<Client, "logoUrl">): string | undefined {
  const url = client.logoUrl;
  if (!url) return undefined;
  try {
    return new URL(url).protocol === "https:" ? url : undefined;
  } catch {
    return undefined;
  }
}

/** "claude.ai" for a verified client; for one that registered itself (shown with an Unverified badge), that it did and the host it claims. */
export function clientHostText(client: Pick<Client, "kind" | "host">): string {
  if (client.kind === "metadata") return client.host || "Verified host";
  return client.host ? `It registered itself and says it's from ${client.host}` : "It registered itself";
}

export function ClientHost({ client }: { client: Pick<Client, "kind" | "host"> }) {
  return <span className={client.kind === "metadata" ? s.mono : undefined}>{clientHostText(client)}</span>;
}
