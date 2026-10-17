/* Admin → Calls › one call: its metadata, and its recorded contents on request (audited as a platform view). */
import { useParams } from "@tanstack/react-router";
import { CallPage } from "@/components/gateways/call-page";

export function AdminCallPage() {
  const { callId } = useParams({ from: "/app/admin/calls/$callId" });
  return <CallPage scope={{ kind: "admin" }} callId={callId} canViewPayload />;
}
