/* Admin → Usage: every gateway's and server's calls from the hourly totals, optionally one team's (platform admins and auditors). */
import { UsageDashboard } from "@/components/usage/usage-dashboard";
import { Stack } from "@/components/ui/layout/layout";
import { PageHeader } from "@/components/ui/page-header/page-header";
import { terms } from "@/lib/terms";
import s from "../../shared.module.css";
import { useTeamOptions } from "../gateways/list";

export function AdminUsagePage() {
  const teams = useTeamOptions();
  return (
    <Stack gap={6} className={s.page}>
      <PageHeader title={terms.usage} description="Calls through every gateway: how many, how they went and how long they took, by gateway, server, capability, team or kind of caller. No grouping by person." />
      <UsageDashboard scope={{ kind: "admin" }} teamOptions={teams.data} />
    </Stack>
  );
}
