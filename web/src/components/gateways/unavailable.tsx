/* The callout for selected capabilities a gateway doesn't serve right now, each with why. */
import { Alert } from "@/components/ui/alert/alert";
import { type GatewayCapability, unavailableHelp } from "./labels";
import g from "./gateways.module.css";

export function UnavailableCallout({ items }: { items: GatewayCapability[] }) {
  const off = items.filter((c) => !c.available);
  if (off.length === 0) return null;
  return (
    <Alert tone="warning" title={off.length === 1 ? "1 capability isn't available right now" : `${off.length} capabilities aren't available right now`}>
      <p>Clients don&apos;t see these until the reason goes away. They stay selected.</p>
      <ul className={g.tight} aria-label="Unavailable capabilities">
        {off.map((c) => (
          <li key={c.capabilityId}>
            <code className={g.code}>{c.exposedName}</code>: {c.unavailableReason ? unavailableHelp[c.unavailableReason] : "Not served right now."}
          </li>
        ))}
      </ul>
    </Alert>
  );
}
