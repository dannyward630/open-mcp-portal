/*
 * Admin › Automation tokens (/admin/automation-tokens): the platform-admin tokens (ompadm_…) for scripts and CI that
 * read the platform or provision teams and gateways. Platform admins create and revoke; auditors read. Team
 * publisher tokens belong to teams: each team's owners and admins manage them in Team settings, and platform staff
 * can read a team's list there, so this page only says so.
 */
import { Link } from "@tanstack/react-router";
import { TokensPanel } from "@/components/tokens/panel";
import { Alert } from "@/components/ui/alert/alert";
import { Stack } from "@/components/ui/layout/layout";
import { PageHeader } from "@/components/ui/page-header/page-header";
import { TextLink } from "@/components/ui/text-link/text-link";
import { adminOnly, terms } from "@/lib/terms";
import s from "../../shared.module.css";
import { useIsPlatformAdmin } from "../hooks";

export function AdminAutomationTokensPage() {
  const isAdmin = useIsPlatformAdmin();
  return (
    <Stack gap={6} className={s.page}>
      <PageHeader
        title={terms.automationTokens}
        description={`Tokens for scripts and CI that call the platform admin API without a browser. ${isAdmin ? "" : adminOnly}`.trim()}
      />
      <TokensPanel
        scope={{ kind: "platform" }}
        canCreate={isAdmin}
        canRevoke={isAdmin}
        title="Platform-admin tokens"
        description="A token acts as a platform auditor (read) or as a platform admin for provisioning only (write). It stops working while the person who made it isn't an active platform admin."
        note={isAdmin ? undefined : adminOnly}
      />
      <Alert tone="info" title="Team publisher tokens">
        Each team&apos;s owners and admins create publisher tokens (omppub_…) for their pipelines under Team settings › Automation tokens; they can register, refresh and publish that team&apos;s servers, nothing
        else. They aren&apos;t listed here: open a team in <TextLink render={<Link to="/admin/teams" />}>Teams</TextLink> and its Team settings to read its tokens. The expiring ones show on the Overview.
      </Alert>
    </Stack>
  );
}
