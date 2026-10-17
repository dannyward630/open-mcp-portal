/*
 * A gateway's Overview tab: what it offers (tiles opening its other tabs),
 * the endpoint with its switch, the capabilities it can't serve right now,
 * and how to connect a client. Connect shows only when the person can use
 * the gateway (it's in their My gateways): platform staff reading a team
 * gateway they aren't on, or a platform gateway not granted to them, are
 * told who can connect instead.
 */
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { AlertTriangle, ListTree, Power, Users } from "lucide-react";
import { Card } from "@/components/ui/card/card";
import { CopyField } from "@/components/ui/copy-field/copy-field";
import { DescriptionList } from "@/components/ui/description-list/description-list";
import { StatCard } from "@/components/ui/stat-card/stat-card";
import { TextLink } from "@/components/ui/text-link/text-link";
import { Time } from "@/components/ui/time/time";
import { myGatewaysQuery } from "../../api/queries";
import { rateText } from "../../lib/limits";
import { switchLabel } from "../../lib/terms";
import { CaptureBadge } from "../capture";
import { useCurrentUser } from "../../session";
import s from "../../pages/shared.module.css";
import { EnabledBadge } from "./badges";
import { ConnectSection } from "./connect";
import { subjectName } from "./add-grant-dialog";
import { type Gateway, type GatewayGrant, grantCountLabel, ownerKindLabels } from "./labels";
import { gatewayCapabilitiesQuery, type GatewayScope, grantsQuery } from "./scope";
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
              ...(gateway.effectiveRateLimitPerMinute !== undefined
                ? [{ label: "Rate limit", value: `${rateText(gateway.effectiveRateLimitPerMinute)}${gateway.rateLimitPerMinute === null ? " (inherited)" : ""}` }]
                : []),
              ...(gateway.capturePayloads !== undefined
                ? [{ label: "Call contents", value: gateway.capturePayloads ? <CaptureBadge label="Recorded" /> : "Not recorded (metadata only)" }]
                : []),
              { label: "Created", value: <Time value={gateway.createdAt} /> },
              { label: "Last changed", value: <Time value={gateway.updatedAt} /> },
            ]}
          />
        </div>
      </Card>
      <Connect gateway={gateway} scope={scope} />
    </>
  );
}

/** Connect when the person can use the gateway; otherwise who can. */
function Connect({ gateway, scope }: Props) {
  const mine = useQuery(myGatewaysQuery());
  if (!gateway.enabled) {
    return (
      <Card title="Connect">
        <p className={g.note}>The gateway is turned off: nobody can connect to it until it&apos;s enabled again.</p>
      </Card>
    );
  }
  if (mine.isLoading) return null;
  // The list failed to load: show the snippets rather than wrongly saying the person can't connect.
  if (mine.error || mine.data?.some((x) => x.id === gateway.id)) return <ConnectSection slug={gateway.slug} url={gateway.url} hideUrl />;
  return (
    <Card title="Connect">
      <div className={g.tight}>
        <p className={g.lead}>You can&apos;t connect to this gateway yourself.</p>
        {gateway.ownerKind === "team" ? <TeamWhoCan gateway={gateway} /> : <GrantsWhoCan gateway={gateway} scope={scope} />}
      </div>
    </Card>
  );
}

function TeamWhoCan({ gateway }: { gateway: Gateway }) {
  const me = useCurrentUser();
  const team = gateway.teamName ?? "its team";
  // A member who doesn't have it: the team is archived (its gateways serve nobody until it's unarchived).
  if (me.teams.some((t) => t.slug === gateway.teamSlug)) {
    return <p className={g.note}>{`It serves nobody while ${team} is archived. Members of ${team} can connect again once the team is unarchived.`}</p>;
  }
  return <p className={g.note}>{`Only members of ${team} can connect to it. Platform admins and auditors can view it, but need to join the team to use it.`}</p>;
}

/** "Teams: Search Infra; people: Ada; SSO groups: engineering". */
export function grantSummary(grants: GatewayGrant[]): string {
  const of = (kind: GatewayGrant["subjectKind"]) => grants.filter((x) => x.subjectKind === kind).map(subjectName);
  const parts = [
    ["teams", of("team")],
    ["people", of("user")],
    ["SSO groups", of("group")],
  ] as const;
  return parts
    .filter(([, names]) => names.length > 0)
    .map(([label, names]) => `${label}: ${names.join(", ")}`)
    .join("; ");
}

function GrantsWhoCan({ gateway, scope }: Props) {
  const grants = useQuery({ ...grantsQuery(gateway.id), enabled: scope.kind === "admin" });
  const access = <Link to="." search={{ tab: "access" } as never} />;
  if (gateway.grantCount === 0) {
    return (
      <p className={g.note}>
        It isn&apos;t granted to anyone yet, so nobody can connect. Teams, people and SSO groups can connect once it&apos;s granted to them.{" "}
        <TextLink render={access}>See its access</TextLink>
      </p>
    );
  }
  const summary = grants.data ? grantSummary(grants.data) : "";
  return (
    <p className={g.note}>
      {summary ? `Only the teams, people and SSO groups it's granted to can connect (${summary}).` : `Only the teams, people and SSO groups it's granted to can connect (${grantCountLabel(gateway.grantCount)}).`}{" "}
      <TextLink render={access}>See its access</TextLink>
    </p>
  );
}
