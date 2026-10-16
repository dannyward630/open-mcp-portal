/*
 * The allowlist entries as a table: kind, value (with the subdomain rule and
 * warnings), plain http and note, and, for editable lists, a row menu with
 * Edit and Remove. The deployment's entries use it read-only.
 */
import { Pencil, Trash2 } from "lucide-react";
import { ActionMenu } from "@/components/templates/action-menu";
import { Badge } from "@/components/ui/badge/badge";
import { CellText } from "@/components/ui/data-table/data-table";
import { Table, Td, Tr } from "@/components/ui/table/table";
import s from "../../shared.module.css";
import { entryWarnings, hostScope, kindLabels, type NetworkEntry } from "./entries";

type Props = {
  caption: string;
  entries: NetworkEntry[];
  empty: string;
  onEdit?: (index: number) => void;
  onRemove?: (index: number) => void;
  /** A server refusal for one entry (422 invalid_entry), shown on its row. */
  problem?: { index: number; message: string };
};

export function EntryTable({ caption, entries, empty, onEdit, onRemove, problem }: Props) {
  const editable = Boolean(onEdit && onRemove);
  return (
    <Table
      caption={caption}
      columns={["Network or host", "Kind", "Plain http", "Note", ...(editable ? [{ label: "", stickyEnd: true }] : [])]}
      stack
      empty={entries.length === 0 ? <p className={s.muted}>{empty}</p> : undefined}
    >
      {entries.map((e, i) => {
        const broad = entryWarnings({ ...e, allowHttp: false }).length > 0;
        return (
          <Tr key={`${e.kind} ${e.value}`}>
            <Td>
              <CellText wrap="anywhere" primary={<span className={s.mono}>{e.value}</span>} secondary={e.kind === "host" ? hostScope(e.value) : undefined} />
              {broad && (
                <span className={s.badges}>
                  <Badge tone="warning">Very broad</Badge>
                </span>
              )}
              {problem?.index === i && <span className={s.dangerText}>{problem.message}</span>}
            </Td>
            <Td>{kindLabels[e.kind]}</Td>
            <Td>{e.allowHttp ? <Badge tone="warning">Allowed</Badge> : <span className={s.muted}>https only</span>}</Td>
            <Td>{e.note ? <CellText wrap="anywhere" primary={e.note} /> : <span className={s.muted}>—</span>}</Td>
            {editable && (
              <Td stickyEnd>
                <ActionMenu
                  label={`Actions for ${e.value}`}
                  actions={[
                    { label: "Edit…", icon: <Pencil aria-hidden />, onSelect: () => onEdit?.(i) },
                    { label: "Remove", icon: <Trash2 aria-hidden />, danger: true, onSelect: () => onRemove?.(i) },
                  ]}
                />
              </Td>
            )}
          </Tr>
        );
      })}
    </Table>
  );
}
