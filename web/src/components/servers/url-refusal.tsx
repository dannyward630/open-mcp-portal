/*
 * Why the gateway refused a server URL for an internal (private) address:
 * platform admins get a link to Admin › Internal networks, where they can
 * allow the network; everyone else is told to ask one.
 */
import { Link } from "@tanstack/react-router";
import { useCurrentUser } from "../../session";
import { TextLink } from "@/components/ui/text-link/text-link";
import { terms } from "@/lib/terms";

export function PrivateAddressRefusal() {
  const isAdmin = useCurrentUser().capabilities.platformAdmin;
  return isAdmin ? (
    <>
      The gateway won&apos;t connect to this internal (private) address until it&apos;s allowed. Allow its network or host in{" "}
      <TextLink render={<Link to="/admin/networks" />}>{terms.internalNetworks}</TextLink>, or use the server&apos;s public address.
    </>
  ) : (
    <>The gateway won&apos;t connect to this internal (private) address. Ask a platform admin to allow its network, or use the server&apos;s public address.</>
  );
}
