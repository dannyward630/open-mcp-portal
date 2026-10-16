/*
 * The picker's options grouped by server (with where each server comes
 * from: the team's own or the catalog), each a checkbox named by the name
 * (tools, prompts) or URI (resources, templates) clients will see; on team
 * gateways a selected one gets a minimum team role.
 */
import { Badge } from "@/components/ui/badge/badge";
import { Checkbox } from "@/components/ui/checkbox/checkbox";
import { NativeSelect } from "@/components/ui/input/input";
import { SourceBadge } from "./badges";
import { groupBy } from "./capability-list";
import { type Gateway, type GatewayCapability, type GatewayKind, type GatewayOption, gatewayKindLabels, minRoleChoices, minRoleLabels, type TeamRole } from "./labels";
import g from "./gateways.module.css";

/** The selection: capability ID → minimum role ("" = every member). */
export type PickerSelection = Record<string, TeamRole | "">;

/** Options matching the search text (name, title, description, server, team), the kind and the server ("" = any). */
export function filterOptions(options: GatewayOption[], text: string, kind: GatewayKind | "", only?: PickerSelection, server = "") {
  const q = text.trim().toLowerCase();
  return options.filter(
    (o) =>
      (!kind || o.kind === kind) &&
      (!server || o.serverId === server) &&
      (!only || o.capabilityId in only) &&
      (!q || [o.exposedName, o.title, o.description, o.serverName, o.serverSlug, o.teamName].some((v) => v.toLowerCase().includes(q))),
  );
}

/** A selected capability as an option (for selected ones not on a loaded page). */
export function optionOf(c: GatewayCapability, gateway: Pick<Gateway, "ownerKind" | "teamSlug">): GatewayOption {
  const own = gateway.ownerKind === "team" && c.teamSlug === gateway.teamSlug;
  return {
    capabilityId: c.capabilityId,
    serverId: c.serverId,
    serverSlug: c.serverSlug,
    serverName: c.serverName,
    teamSlug: c.teamSlug,
    teamName: c.teamSlug,
    source: own ? "team" : "catalog",
    kind: c.kind,
    name: c.name,
    exposedName: c.exposedName,
    title: c.title,
    description: c.description,
  };
}

type Props = {
  options: GatewayOption[];
  selection: PickerSelection;
  /** Minimum roles (team gateways). */
  roles: boolean;
  /** What's wrong with a selected capability after a refused save. */
  problems: Map<string, string>;
  onToggle: (id: string, on: boolean) => void;
  onRole: (id: string, role: TeamRole | "") => void;
};

export function OptionGroups({ options, selection, roles, problems, onToggle, onRole }: Props) {
  return (
    <div className={g.stack}>
      {groupBy(options, (o) => o.serverId).map(([id, opts]) => {
        const first = opts[0]!;
        const chosen = opts.filter((o) => o.capabilityId in selection).length;
        return (
          <section key={id} className={g.group} aria-labelledby={`option-server-${id}`}>
            <h3 id={`option-server-${id}`} className={g.groupHead}>
              {first.serverName}
              <SourceBadge source={first.source} />
              <span className={g.groupMeta}>
                {first.source === "catalog" ? `${first.teamName} · ` : ""}
                {chosen > 0 ? `${chosen} of ${opts.length} selected` : `${opts.length} available`}
              </span>
            </h3>
            <ul className={g.list}>
              {opts.map((o) => (
                <OptionRow key={o.capabilityId} option={o} role={selection[o.capabilityId]} roles={roles} problem={problems.get(o.capabilityId)} onToggle={onToggle} onRole={onRole} />
              ))}
            </ul>
          </section>
        );
      })}
    </div>
  );
}

type RowProps = Pick<Props, "roles" | "onToggle" | "onRole"> & { option: GatewayOption; role: TeamRole | "" | undefined; problem?: string };

function OptionRow({ option: o, role, roles, problem, onToggle, onRole }: RowProps) {
  const selected = role !== undefined;
  const description = [o.title && o.title !== o.name ? o.title : "", o.description].filter(Boolean).join(" — ");
  return (
    <li className={g.row} data-problem={problem ? "" : undefined}>
      <div className={g.rowMain}>
        <Checkbox
          label={<code className={g.exposed}>{o.exposedName}</code>}
          description={description || undefined}
          checked={selected}
          onCheckedChange={(on) => onToggle(o.capabilityId, on)}
        />
        {problem && <p className={g.problem}>{problem}</p>}
      </div>
      <div className={g.rowSide}>
        <Badge size="sm" variant="outline">
          {gatewayKindLabels[o.kind].one}
        </Badge>
        {roles && selected && (
          <NativeSelect
            size="sm"
            className={g.roleSelect}
            aria-label={`Who may use ${o.exposedName}`}
            value={role === "member" ? "" : role}
            onChange={(e) => onRole(o.capabilityId, e.target.value as TeamRole | "")}
          >
            {minRoleChoices.map((r) => (
              <option key={r} value={r}>
                {minRoleLabels[r]}
              </option>
            ))}
          </NativeSelect>
        )}
      </div>
    </li>
  );
}
