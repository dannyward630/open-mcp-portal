/*
 * One of the team's call records (/teams/$team/calls/$callId): any call the
 * team's call lists show (owners, admins and platform staff). Its recorded
 * contents open here for the team's owners and admins; platform staff open
 * them in Admin, where the view is audited as theirs.
 */
import { Link, useParams } from "@tanstack/react-router";
import { CallPage } from "../../../components/gateways/call-page";
import { TextLink } from "@/components/ui/text-link/text-link";
import { useTeam } from "../common";

export function TeamCallPage() {
  const { callId } = useParams({ from: "/app/teams/$team/calls/$callId" });
  const { slug, role } = useTeam();
  const manager = role === "owner" || role === "admin";
  return (
    <CallPage
      scope={{ kind: "team", team: slug }}
      callId={callId}
      canViewPayload={manager}
      elsewhere={
        <>
          Its contents were recorded. Platform staff view them in Admin:{" "}
          <TextLink render={<Link to="/admin/calls/$callId" params={{ callId }} />}>open this call in Admin</TextLink>.
        </>
      }
    />
  );
}
