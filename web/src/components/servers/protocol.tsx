/*
 * The MCP protocol version the portal last negotiated with a server (set by refresh, health checks and test), on the
 * server's Overview tab: "Protocol 2026-07-28 (modern), negotiated 3 hours ago". Modern is 2026-07-28 and later
 * (stateless), legacy is 2025-11-25 and earlier (initialize). Nothing yet, or a server from before the portal
 * recorded it: "Not negotiated yet".
 */
import type { Schemas } from "../../api/client";
import { Time } from "@/components/ui/time/time";

type Protocol = Schemas["McpServerProtocol"];

const eraTitles: Record<Protocol["era"], string> = {
  modern: "Modern: protocol version 2026-07-28 or later (stateless requests).",
  legacy: "Legacy: protocol version 2025-11-25 or earlier (initialize handshake).",
};

export function ProtocolText({ protocol }: { protocol?: Protocol | null }) {
  if (!protocol) return <>Not negotiated yet</>;
  return (
    <span title={eraTitles[protocol.era]}>
      Protocol {protocol.version} ({protocol.era}), negotiated <Time value={protocol.negotiatedAt} format="relative" />
    </span>
  );
}
