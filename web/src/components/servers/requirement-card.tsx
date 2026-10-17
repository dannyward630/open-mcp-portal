/*
 * What a server receives on behalf of its callers (decision 26), for its
 * Overview and a platform admin's review: the provider and scopes whose user
 * token calls carry (in MCP-Gateway-Provider-Token, with the provider's
 * slug in MCP-Gateway-Provider), and each capability that differs from it.
 */
import { useQuery } from "@tanstack/react-query";
import { needsText, scopesText, serviceAccountsCant } from "../providers/labels";
import { Card } from "@/components/ui/card/card";
import { DescriptionList } from "@/components/ui/description-list/description-list";
import s from "../../pages/shared.module.css";
import type { McpServer } from "./labels";
import { overrideLabels } from "./requirement";
import { capabilitiesQuery, type ServerScope } from "./scope";
import m from "./servers.module.css";

/** The capabilities whose requirement differs from the server's, as "name: No account" lines. */
function useOverrides(server: McpServer, scope: ServerScope) {
  const caps = useQuery(capabilitiesQuery(scope, server.id));
  return (caps.data ?? [])
    .filter((c) => !c.goneAt && c.requirementOverride.mode !== "inherit")
    .map((c) => ({ id: c.id, name: c.name, text: c.requires ? needsText(c.requires) : overrideLabels[c.requirementOverride.mode] }));
}

/** The requirement and its overrides as a list (the review dialog's and the card's body). */
export function RequirementSummary({ server, scope }: { server: McpServer; scope: ServerScope }) {
  const overrides = useOverrides(server, scope);
  const r = server.requirement;
  if (!r) return <p className={s.muted}>None: calls carry only the gateway&apos;s signed identity token, never a person&apos;s account.</p>;
  return (
    <DescriptionList
      dividers
      items={[
        { label: "Provider", value: `${r.providerName}${r.providerEnabled ? "" : " (turned off: calls that need it are refused)"}` },
        { label: "Scopes", value: <span className={s.mono}>{scopesText(r.scopes, "The provider's default scopes")}</span> },
        {
          label: "Sent to the server",
          value: (
            <span className={m.stack}>
              <span>
                The caller&apos;s access token in <code className={s.mono}>MCP-Gateway-Provider-Token</code>, with <code className={s.mono}>MCP-Gateway-Provider: {r.providerSlug}</code>.
              </span>
              <span className={s.secondary}>{serviceAccountsCant}</span>
            </span>
          ),
        },
        {
          label: "Capabilities that differ",
          value: overrides.length ? (
            <ul className={s.inUseList}>
              {overrides.map((o) => (
                <li key={o.id}>
                  <code className={s.mono}>{o.name}</code>: {o.text}
                </li>
              ))}
            </ul>
          ) : (
            "None: every capability gets the server's"
          ),
        },
      ]}
    />
  );
}

export function RequirementCard({ server, scope }: { server: McpServer; scope: ServerScope }) {
  return (
    <Card
      title="User authorization"
      description={server.requirement ? `Calls carry each caller's own ${server.requirement.providerName} token. Reviewers check what the server receives.` : undefined}
    >
      <RequirementSummary server={server} scope={scope} />
    </Card>
  );
}
