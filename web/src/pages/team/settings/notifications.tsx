/*
 * Team settings › Notifications: the team's webhook destinations (a chat channel, your own receiver), for owners and
 * admins to manage; platform staff read them (and the delivery log). Editors and members can't list destinations
 * (a Slack URL is a credential): they're told who can, and where their own notifications are chosen.
 */
import { Link } from "@tanstack/react-router";
import { Lock } from "lucide-react";
import { DestinationsPanel } from "../../../components/notifications/destinations/panel";
import { useCurrentUser } from "../../../session";
import { Card } from "@/components/ui/card/card";
import { EmptyState } from "@/components/ui/empty-state/empty-state";
import { TextLink } from "@/components/ui/text-link/text-link";
import { useTeam } from "../common";

export function TeamNotificationsTab() {
  const { slug, archived, isManager } = useTeam();
  const me = useCurrentUser();
  const staff = me.capabilities.platformAdmin || me.capabilities.platformAuditor;
  if (!isManager && !staff) {
    return (
      <Card>
        <EmptyState
          icon={<Lock />}
          title="Only the team's owners and admins manage webhooks."
          description={
            <>
              Webhooks send the team&apos;s events (a failing server, a changed capability, an expiring key) to a chat channel or a receiver. Ask an owner or admin to add one. What reaches you
              yourself is chosen in <TextLink render={<Link to="/settings/notifications" />}>your notification settings</TextLink>.
            </>
          }
        />
      </Card>
    );
  }
  return (
    <DestinationsPanel
      scope={{ kind: "team", team: slug }}
      canManage={isManager && !archived}
      description="Send the team's events to a chat channel or your own receiver: failing and recovered servers, changed capabilities, review decisions, sign-in lapses and expiring keys. Members get theirs in the portal and by email (their own settings)."
      readOnlyNote={archived ? "The team is archived, so its destinations can't be changed." : !isManager ? "You're viewing this team as platform staff: destinations can be read, not changed." : undefined}
    />
  );
}
