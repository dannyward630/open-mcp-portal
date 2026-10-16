/* Badges of gateways: availability, source, owner, unavailable counts and call outcomes. */
import { Badge, StatusBadge } from "@/components/ui/badge/badge";
import { type CallOutcome, type GatewayCapability, type GatewayOption, outcomeHelp, outcomeLabels, outcomeTone, unavailableHelp, unavailableLabels } from "./labels";

export { EnabledBadge } from "../servers/badges";

/** "Available", or why it isn't ("Server disabled"), with the reason as a tooltip. */
export function AvailabilityBadge({ cap }: { cap: Pick<GatewayCapability, "available" | "unavailableReason"> }) {
  if (cap.available) return <Badge tone="success">Available</Badge>;
  const reason = cap.unavailableReason;
  return (
    <Badge tone="warning" title={reason ? unavailableHelp[reason] : undefined}>
      {reason ? unavailableLabels[reason] : "Unavailable"}
    </Badge>
  );
}

/** Where a selectable capability comes from: the team's own server or the catalog. */
export function SourceBadge({ source }: { source: GatewayOption["source"] }) {
  return source === "team" ? (
    <Badge tone="info" size="sm">
      Team
    </Badge>
  ) : (
    <Badge size="sm" variant="outline">
      Catalog
    </Badge>
  );
}

/** "2 unavailable" next to a gateway's capability count. */
export function UnavailableCountBadge({ count }: { count: number }) {
  if (count <= 0) return null;
  return (
    <Badge tone="warning" size="sm">
      {count.toLocaleString()} unavailable
    </Badge>
  );
}

export function OutcomeBadge({ outcome }: { outcome: CallOutcome }) {
  return (
    <StatusBadge tone={outcomeTone[outcome]} title={outcomeHelp[outcome]}>
      {outcomeLabels[outcome]}
    </StatusBadge>
  );
}
