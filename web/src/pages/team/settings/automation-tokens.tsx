/*
 * Team settings › Automation tokens: publisher tokens (omppub_…) for the team's CI: they can register, refresh and
 * publish the team's servers, never approve, delete or change anything else. Owners and admins create and revoke;
 * platform staff read. Editors and members can't list tokens: they're told who can. An archived team's tokens can
 * be revoked, not created.
 */
import { KeyRound } from "lucide-react";
import { TokensPanel } from "../../../components/tokens/panel";
import { useCurrentUser } from "../../../session";
import { Card } from "@/components/ui/card/card";
import { EmptyState } from "@/components/ui/empty-state/empty-state";
import { useTeam } from "../common";

export function TeamAutomationTokensTab() {
  const { slug, archived, isManager } = useTeam();
  const me = useCurrentUser();
  const staff = me.capabilities.platformAdmin || me.capabilities.platformAuditor;
  if (!isManager && !staff) {
    return (
      <Card>
        <EmptyState
          icon={<KeyRound />}
          title="Only the team's owners and admins see automation tokens."
          description="A publisher token lets a CI pipeline register, refresh and publish this team's servers. Ask an owner or admin of the team if you need one."
        />
      </Card>
    );
  }
  return (
    <TokensPanel
      scope={{ kind: "team", team: slug }}
      canCreate={isManager && !archived}
      canRevoke={isManager}
      title="Publisher tokens"
      description="For this team's pipeline: register, refresh and publish its servers. A token can publish but not approve or delete, and it keeps working if the person who made it leaves the team."
      note={archived ? "The team is archived: tokens can be revoked, not created." : !isManager ? "You're viewing this team as platform staff: tokens can be read, not changed." : undefined}
    />
  );
}
