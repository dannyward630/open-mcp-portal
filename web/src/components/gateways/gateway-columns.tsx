/*
 * The columns of a list of gateways (the team's and Admin's): name, switch,
 * what it offers and what it can't serve now. On a phone the owner and
 * capability count start hidden so the switch stays in view; the unavailable
 * count moves next to it.
 */
import type { ReactElement } from "react";
import { timeColumn } from "../templates/list-page";
import { CellText, type DataTableColumn } from "@/components/ui/data-table/data-table";
import { TextLink } from "@/components/ui/text-link/text-link";
import { switchLabel } from "../../lib/terms";
import s from "../../pages/shared.module.css";
import { EnabledBadge, UnavailableCountBadge } from "./badges";
import { capabilityCount, type Gateway, grantCountLabel, ownerKindLabels } from "./labels";

/** `narrow`: a phone-width window (NARROW_QUERY). */
export function gatewayColumns(link: (x: Gateway) => ReactElement, opts: { admin?: boolean; narrow?: boolean } = {}): DataTableColumn<Gateway>[] {
  return [
    {
      id: "name",
      header: "Gateway",
      accessor: (x) => x.name,
      rowHeader: true,
      hideable: false,
      sortable: true,
      cell: (x) => <CellText className={s.compactCell} wrap="anywhere" primary={<TextLink render={link(x)}>{x.name}</TextLink>} secondary={<span className={s.mono}>{x.slug}</span>} />,
    },
    ...(opts.admin
      ? [
          {
            id: "owner",
            header: "Owner",
            accessor: (x: Gateway) => x.teamName ?? ownerKindLabels[x.ownerKind],
            sortable: true,
            defaultHiddenNarrow: true,
            cell: (x: Gateway) =>
              x.ownerKind === "team" ? <CellText primary={x.teamName ?? x.teamSlug} secondary={ownerKindLabels.team} /> : <CellText primary="Platform" secondary={grantCountLabel(x.grantCount)} />,
          },
        ]
      : []),
    {
      id: "status",
      header: "Status",
      accessor: (x) => switchLabel(x.enabled),
      sortable: true,
      cell: (x) =>
        opts.narrow ? (
          <span className={s.badges}>
            <EnabledBadge enabled={x.enabled} />
            <UnavailableCountBadge count={x.unavailableCount} />
          </span>
        ) : (
          <EnabledBadge enabled={x.enabled} />
        ),
    },
    {
      id: "capabilities",
      header: "Capabilities",
      accessor: (x) => x.capabilityCount,
      sortable: true,
      defaultHiddenNarrow: true,
      cell: (x) => (
        <span className={s.badges}>
          <span className={s.nowrap}>{capabilityCount(x.capabilityCount)}</span>
          <UnavailableCountBadge count={x.unavailableCount} />
        </span>
      ),
    },
    { ...timeColumn<Gateway>("updatedAt", "Updated", (x) => x.updatedAt), defaultHiddenNarrow: true },
  ];
}
