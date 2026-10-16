/*
 * A service account's Overview: its role (what minimum roles it passes),
 * who answers for it, and what its keys reach (decision 23): the team's own
 * gateways, linked, and the platform gateways granted to the team (a team
 * grant covers its service accounts; there are no grants to one account).
 */
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { api, unwrap } from "../../../api/client";
import { teamGatewaysKey } from "../../../api/queries";
import { EnabledBadge } from "../../../components/servers/badges";
import { capabilityCount } from "../../../components/gateways/labels";
import { terms } from "../../../lib/terms";
import { useCurrentUser } from "../../../session";
import { ErrorAlert } from "@/components/ui/alert/alert";
import { Badge } from "@/components/ui/badge/badge";
import { Card } from "@/components/ui/card/card";
import { DescriptionList } from "@/components/ui/description-list/description-list";
import { Loading } from "@/components/ui/spinner/spinner";
import { Table, Td, Tr } from "@/components/ui/table/table";
import { TextLink } from "@/components/ui/text-link/text-link";
import { Time } from "@/components/ui/time/time";
import s from "../../shared.module.css";
import { useTeam } from "../common";
import { roleExplanation, roleLabel, type ServiceAccount, type ServiceAccountAbilities, serviceAccountRoleHelp } from "./labels";

type Props = { account: ServiceAccount; abilities: ServiceAccountAbilities };

export function OverviewTab({ account }: Props) {
  const { team } = useTeam();
  return (
    <>
      <Card title="Role" description={roleExplanation}>
        <DescriptionList
          dividers
          items={[
            {
              label: "Role",
              value: (
                <span>
                  <Badge tone={account.role === "editor" ? "info" : "neutral"}>{roleLabel(account.role)}</Badge> <span className={s.secondary}>{serviceAccountRoleHelp[account.role]}</span>
                </span>
              ),
            },
            { label: "Responsible", value: `The owners and admins of ${team.name}. The account stays when the person who created it leaves.` },
            { label: "Created", value: <Time value={account.createdAt} /> },
            ...(account.createdByName ? [{ label: "Created by", value: account.createdByName }] : []),
            { label: "Last changed", value: <Time value={account.updatedAt} /> },
          ]}
        />
      </Card>
      <AccessCard account={account} />
    </>
  );
}

function AccessCard({ account }: { account: ServiceAccount }) {
  const { slug, team } = useTeam();
  const me = useCurrentUser();
  const staff = me.capabilities.platformAdmin || me.capabilities.platformAuditor;
  const gateways = useQuery({
    queryKey: [...teamGatewaysKey(slug), "list", "service-account-access"],
    queryFn: async () => unwrap(await api.GET("/v1/teams/{team}/gateways", { params: { path: { team: slug }, query: { limit: 200 } } })),
  });
  const items = gateways.data?.items ?? [];
  return (
    <Card
      title="Access"
      description={`What its keys reach, with its ${roleLabel(account.role).toLowerCase()} role: the gateways of ${team.name}, and the platform gateways granted to ${team.name}. A key can be limited to some of them.`}
      flush={items.length > 0}
    >
      {gateways.isLoading && <Loading label="Loading the team's gateways…" />}
      <ErrorAlert error={gateways.error} title="Couldn't load the team's gateways" />
      {gateways.isSuccess && items.length === 0 && <p className={s.muted}>{`${team.name} has no gateways yet.`}</p>}
      {items.length > 0 && (
        <Table caption={`Gateways of ${team.name}`} columns={["Gateway", "Status", "Capabilities"]} stack>
          {items.map((g) => (
            <Tr key={g.id}>
              <Td>
                <TextLink render={<Link to="/teams/$team/gateways/$gatewayId" params={{ team: slug, gatewayId: g.id }} />} className={s.primary}>
                  {g.name}
                </TextLink>
                <span className={`${s.secondary} ${s.mono}`}>{g.slug}</span>
              </Td>
              <Td>
                <EnabledBadge enabled={g.enabled} />
              </Td>
              <Td>{capabilityCount(g.capabilityCount)}</Td>
            </Tr>
          ))}
        </Table>
      )}
      <p className={items.length > 0 ? `${s.muted} ${s.pad}` : s.muted}>
        {`Platform gateways granted to ${team.name} work for it too; a grant to the team covers its service accounts. `}
        {staff ? (
          <TextLink render={<Link to="/admin/gateways" search={{ ownerKind: "platform" }} />}>See platform gateways in Admin › {terms.gateways}</TextLink>
        ) : (
          "A platform admin can tell you which ones."
        )}
      </p>
    </Card>
  );
}
