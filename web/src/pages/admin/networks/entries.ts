/*
 * Internal networks: the allowlist entries' words, the client-side checks
 * that catch typos before Save (the server canonicalises and has the last
 * word), and the warnings for very broad networks and plain http.
 */
import type { Schemas } from "@/api/client";

export type NetworkEntry = Schemas["NetworkEntry"];
export type NetworkSettings = Schemas["NetworkSettings"];
export type NetworkKind = NetworkEntry["kind"];
export type NetworkCheck = Schemas["NetworkCheckResult"];

export const networksKey = ["admin", "networks"];

export const maxEntries = 100;
export const maxNote = 200;

export const kindLabels: Record<NetworkKind, string> = { cidr: "Network (CIDR)", host: "Host name" };

/** An entry in the server's field order, without an empty note, so lists compare equal to what the API returns. */
export function normalEntry(e: NetworkEntry): NetworkEntry {
  const note = e.note?.trim();
  return { allowHttp: e.allowHttp, kind: e.kind, ...(note ? { note } : {}), value: e.value };
}

/** A list's identity for "has it changed": kind, value, http and note, whatever the key order. */
export const listKey = (entries: NetworkEntry[]) => JSON.stringify(entries.map((e) => [e.kind, e.value, e.allowHttp, e.note?.trim() ?? ""]));

/** The value as the server stores it, as far as the browser can tell: trimmed, hosts lower-case without a trailing dot. */
export function tidyValue(kind: NetworkKind, value: string): string {
  const v = value.trim();
  return kind === "host" ? v.toLowerCase().replace(/\.$/, "") : v.toLowerCase();
}

const ipv4 = /^(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}$/;
const label = "[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?";
const hostName = new RegExp(`^(\\*\\.)?(${label}\\.)*${label}$`);

/** The prefix length and family of a CIDR value ("10.0.0.0/8" → 8, v4), or null when it isn't one. */
export function parseCidr(value: string): { v4: boolean; bits: number } | null {
  const [addr = "", bits, ...rest] = value.trim().split("/");
  if (rest.length > 0) return null;
  const v4 = !addr.includes(":");
  const v6ok = /^[0-9a-f:.]+$/i.test(addr) && (addr.includes("::") || addr.split(":").length >= 3);
  if (v4 ? !ipv4.test(addr) : !v6ok) return null;
  const max = v4 ? 32 : 128;
  if (bits === undefined) return { v4, bits: max };
  if (!/^\d{1,3}$/.test(bits) || Number(bits) > max) return null;
  return { v4, bits: Number(bits) };
}

/** What's wrong with an entry's value, before it reaches the server. */
export function valueProblem(kind: NetworkKind, value: string): string | undefined {
  const v = tidyValue(kind, value);
  if (!v) return kind === "cidr" ? "Enter a network, such as 10.20.0.0/16." : "Enter a host name, such as mcp.corp.example.com.";
  if (kind === "cidr") return parseCidr(v) ? undefined : "Enter a network such as 10.20.0.0/16 or fd00:1234::/32 (an address alone means just that address).";
  if (/[^\x20-\x7e]/.test(v)) return "Enter international names in their ASCII (punycode) form, such as xn--bcher-kva.example.";
  if (/[:/]/.test(v)) return "Enter only the host name: no scheme, port or path.";
  if (parseCidr(v.replace(/^\*\./, ""))) return "This is an address. Add it as a network (CIDR) instead.";
  if (v === "localhost" || v.endsWith(".localhost")) return "localhost is loopback, which MCP servers may never use.";
  if (!hostName.test(v)) return "Enter a host name such as mcp.corp.example.com, or *.corp.example.com for its subdomains.";
  return undefined;
}

/** Why an entry deserves a second look: a very broad network, or plain http. */
export function entryWarnings(e: Pick<NetworkEntry, "kind" | "value" | "allowHttp">): string[] {
  const out: string[] = [];
  const cidr = e.kind === "cidr" ? parseCidr(e.value) : null;
  if (cidr && cidr.v4 && cidr.bits < 8) out.push(`Very broad: /${cidr.bits} covers more than 16 million IPv4 addresses, possibly including networks you didn't mean to open.`);
  if (cidr && !cidr.v4 && cidr.bits < 32) out.push(`Very broad: /${cidr.bits} is wider than a typical organisation's whole IPv6 allocation (/32).`);
  if (e.allowHttp) out.push("Plain http sends requests, the server's header and the caller's identity token unencrypted.");
  return out;
}

/** The subdomain rule in words, for a host entry's row. */
export function hostScope(value: string): string | undefined {
  return value.startsWith("*.") ? `Subdomains of ${value.slice(2)}, not ${value.slice(2)} itself` : undefined;
}

/** Why the server refused an entry (422 invalid_entry, details.reason). */
export const entryReasonLabels: Record<string, string> = {
  invalid_kind: "The kind must be a network (CIDR) or a host name.",
  invalid_cidr: "This isn't a network the portal understands.",
  invalid_host: "This isn't a host name the portal understands.",
  non_ascii: "Use the ASCII (punycode) form of international names.",
  ip_as_host: "Addresses belong in a network (CIDR) entry.",
  always_blocked: "Loopback, link-local, metadata, multicast and broadcast addresses are always blocked.",
  invalid_note: `The note must be at most ${maxNote} characters on one line.`,
  duplicate: "Another entry lists the same network or host.",
  too_many: `At most ${maxEntries} entries.`,
};

/** A URL check's reason, for the result and each address. */
export const checkReasonLabels: Record<string, string> = {
  public: "Public address",
  cidr_entry: "Inside an allowed network",
  host_entry: "Allowed host",
  development: "Development allowance",
  blocked_address: "Always blocked",
  private_address: "Internal, not allowed",
  dns_failed: "Couldn't resolve",
  invalid: "Not a full URL",
  too_long: "URL too long",
  userinfo: "Has a user name or password",
  query: "Has a query",
  fragment: "Has a fragment",
  scheme: "Needs https",
};
