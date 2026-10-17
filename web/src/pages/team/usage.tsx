/*
 * Team › Usage (/teams/$team/usage): the calls the team's call lists cover
 * (through its gateways, to its servers, or by its service accounts), from
 * the hourly totals. Owners, admins and editors, and platform staff; members
 * are told who can see it.
 */
import { Lock } from "lucide-react";
import { UsageDashboard } from "../../components/usage/usage-dashboard";
import { useCurrentUser } from "../../session";
import { Card } from "@/components/ui/card/card";
import { EmptyState } from "@/components/ui/empty-state/empty-state";
import { Stack } from "@/components/ui/layout/layout";
import { PageHeader } from "@/components/ui/page-header/page-header";
import { terms } from "../../lib/terms";
import s from "../shared.module.css";
import { useTeam } from "./common";

/** Who reads a team's usage: its owners, admins and editors, and platform staff. */
export const canReadUsage = (role: string | undefined, staff: boolean) => role === "owner" || role === "admin" || role === "editor" || (!role && staff);

export function TeamUsagePage() {
  const { slug, team, role } = useTeam();
  const me = useCurrentUser();
  const staff = me.capabilities.platformAdmin || me.capabilities.platformAuditor;
  return (
    <Stack gap={6} className={s.page}>
      <PageHeader
        title={terms.usage}
        description={`Calls through ${team.name}'s gateways, to its MCP servers and by its service accounts: how many, how they went and how long they took.`}
      />
      {canReadUsage(role, staff) ? (
        <UsageDashboard scope={{ kind: "team", team: slug }} />
      ) : (
        <Card>
          <EmptyState icon={<Lock />} title="Only the team's owners, admins and editors see its usage." description="Ask one of them if you need these numbers." />
        </Card>
      )}
    </Stack>
  );
}
