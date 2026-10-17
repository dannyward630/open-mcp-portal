/*
 * Team settings › Limits & retention (read-only): the rate limits that apply
 * to the team's service accounts and gateways, and how long its call records
 * and recorded contents are kept. Platform admins set them.
 */
import { useQuery } from "@tanstack/react-query";
import { ErrorAlert } from "@/components/ui/alert/alert";
import { Badge } from "@/components/ui/badge/badge";
import { Card } from "@/components/ui/card/card";
import { Stack } from "@/components/ui/layout/layout";
import { SkeletonText } from "@/components/ui/skeleton/skeleton";
import { Table, Td, Tr } from "@/components/ui/table/table";
import { rateText, teamLimitsQuery } from "../../../lib/limits";
import { daysText, retentionLabels, teamRetentionQuery } from "../../../lib/retention";
import s from "../../shared.module.css";
import { useTeam } from "../common";

const SetBadge = () => (
  <Badge size="sm" variant="outline">
    Set for your team
  </Badge>
);

export function LimitsTab() {
  const { slug, team } = useTeam();
  const limits = useQuery(teamLimitsQuery(slug));
  const retention = useQuery(teamRetentionQuery(slug));
  return (
    <Stack gap={6}>
      <p className={s.note}>Platform admins set these. Ask one if your team needs other values.</p>
      <Card title="Rate limits" description={`Requests per minute to gateways. A gateway of ${team.name} can have its own limit, up to the ceiling (its Settings tab).`}>
        {limits.isLoading ? (
          <SkeletonText lines={4} />
        ) : !limits.data ? (
          <ErrorAlert error={limits.error} title="Couldn't load the rate limits" onRetry={() => void limits.refetch()} />
        ) : (
          <Table caption="Rate limits that apply" stack columns={["Limit", { label: "Applies", width: "12rem" }, { label: "Ceiling", width: "12rem" }]}>
            {limits.data.items.map((l) => (
              <Tr key={l.key}>
                <Td>
                  <span className={s.primary}>{l.label}</span>
                  <span className={s.secondary}>{l.description}</span>
                </Td>
                <Td>
                  {rateText(l.effective)}
                  {l.override !== null && (
                    <span className={s.secondary}>
                      <SetBadge />
                    </span>
                  )}
                </Td>
                <Td muted>{l.teamOverridable ? rateText(l.ceiling, "None") : "—"}</Td>
              </Tr>
            ))}
          </Table>
        )}
      </Card>
      <Card title="Retention" description="How long records are kept before they're deleted. A call that also involves another team is kept for the shorter of the two teams' periods.">
        {retention.isLoading ? (
          <SkeletonText lines={2} />
        ) : !retention.data ? (
          <ErrorAlert error={retention.error} title="Couldn't load the retention periods" onRetry={() => void retention.refetch()} />
        ) : (
          <Table caption="Retention that applies" stack columns={["Records", { label: "Kept for", width: "12rem" }]}>
            {retention.data.periods.map((p) => (
              <Tr key={p.kind}>
                <Td>
                  <span className={s.primary}>{retentionLabels[p.kind].label}</span>
                  <span className={s.secondary}>{retentionLabels[p.kind].description}</span>
                </Td>
                <Td>
                  {daysText(p.effective)}
                  {p.override !== null && (
                    <span className={s.secondary}>
                      <SetBadge />
                    </span>
                  )}
                </Td>
              </Tr>
            ))}
          </Table>
        )}
      </Card>
    </Stack>
  );
}
