/*
 * A gateway's Overview tab: what it offers (tiles opening its other tabs),
 * the endpoint with its switch, the capabilities it can't serve right now,
 * and how to connect a client.
 */
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { AlertTriangle, ListTree, Power, Users } from "lucide-react";
import { Card } from "@/components/ui/card/card";
import { CopyField } from "@/components/ui/copy-field/copy-field";
import { DescriptionList } from "@/components/ui/description-list/description-list";
import { StatCard } from "@/components/ui/stat-card/stat-card";
import { Time } from "@/components/ui/time/time";
import { switchLabel } from "../../lib/terms";
import s from "../../pages/shared.module.css";
import { EnabledBadge } from "./badges";
import { ConnectSection } from "./connect";
import { type Gateway, ownerKindLabels } from "./labels";
import { gatewayCapabilitiesQuery, type GatewayScope } from "./scope";
import { UnavailableCallout } from "./unavailable";
import g from "./gateways.module.css";

type Props = { gateway: Gateway; scope: GatewayScope };

export function GatewayOverviewTab({ gateway, scope }: Props) {
  const caps = useQuery(gatewayCapabilitiesQuery(scope, gateway.id));
  const platform = gateway.ownerKind === "platform";
  const tab = (t: string) => <Link to="." search={{ tab: t } as never} />;
  return (
    <>
      <div className={s.stats3} role="group" aria-label="The gateway at a glance">
        <StatCard label="Capabilities" value={gateway.capabilityCount.toLocaleString()} icon={<ListTree />} render={tab("capabilities")} />
        <StatCard label="Unavailable now" value={gateway.unavailableCount.toLocaleString()} icon={<AlertTriangle />} render={tab("capabilities")} />
        {platform && scope.kind === "admin" ? (
          <StatCard label="Grants" value={gateway.grantCount.toLocaleString()} icon={<Users />} render={tab("access")} />
        ) : (
          <StatCard label="Status" value={switchLabel(gateway.enabled)} icon={<Power />} />
        )}
      </div>
      {caps.data && <UnavailableCallout items={caps.data.items} />}
      <Card
        title="Endpoint"
        description={gateway.enabled ? "MCP clients connect to this URL over Streamable HTTP." : "The gateway is turned off: its endpoint answers “not found” to everyone until it's enabled again."}
      >
        <div className={g.stack}>
          <CopyField label="Endpoint URL" name="endpoint URL" value={gateway.url} />
          <DescriptionList
            dividers
            items={[
              { label: "Status", value: <EnabledBadge enabled={gateway.enabled} /> },
              { label: "Kind", value: ownerKindLabels[gateway.ownerKind] },
              ...(gateway.teamName ? [{ label: "Team", value: gateway.teamName }] : []),
              { label: "Slug", value: <code className={s.mono}>{gateway.slug}</code> },
              { label: "Created", value: <Time value={gateway.createdAt} /> },
              { label: "Last changed", value: <Time value={gateway.updatedAt} /> },
            ]}
          />
        </div>
      </Card>
      <ConnectSection slug={gateway.slug} url={gateway.url} hideUrl />
    </>
  );
}
