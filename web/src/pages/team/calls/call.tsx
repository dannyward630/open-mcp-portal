/*
 * One of the team's call records (/teams/$team/calls/$callId): any call the
 * team's call lists show (owners, admins and platform staff). Its recorded
 * contents open here for the team's owners and admins; platform staff open
 * them in Admin, where the view is audited as theirs. The team's record never
 * says "captured" to platform staff, so their page reads the same call in
 * Admin (metadata only, not audited) and points there when it was recorded.
 */
import { useQuery } from "@tanstack/react-query";
import { Link, useParams } from "@tanstack/react-router";
import { CallPage, callQuery } from "../../../components/gateways/call-page";
import { useCurrentUser } from "../../../session";
import { TextLink } from "@/components/ui/text-link/text-link";
import { useTeam } from "../common";

export function TeamCallPage() {
  const { callId } = useParams({ from: "/app/teams/$team/calls/$callId" });
  const { slug, role } = useTeam();
  const { capabilities } = useCurrentUser();
  const manager = role === "owner" || role === "admin";
  const staff = !manager && (capabilities.platformAdmin || capabilities.platformAuditor);
  const q = callQuery({ kind: "admin" }, Number(callId));
  const adminRecord = useQuery({ ...q, enabled: q.enabled && staff, retry: false });
  return (
    <CallPage
      scope={{ kind: "team", team: slug }}
      callId={callId}
      canViewPayload={manager}
      elsewhere={
        staff && adminRecord.data?.captured ? (
          <>
            Its contents were recorded. Platform staff view them in Admin, where the view is audited as yours:{" "}
            <TextLink render={<Link to="/admin/calls/$callId" params={{ callId }} />}>open this call in Admin</TextLink>.
          </>
        ) : undefined
      }
    />
  );
}
