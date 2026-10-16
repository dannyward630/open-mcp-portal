/*
 * The trusted clients as a table: client ID (with the host that vouches for
 * it) and label, and, for platform admins, a row menu with Edit and Remove.
 * A client the server refused (422 invalid_client_id) says so on its row.
 */
import { Pencil, Trash2 } from "lucide-react";
import { ActionMenu } from "@/components/templates/action-menu";
import { CellText } from "@/components/ui/data-table/data-table";
import { Table, Td, Tr } from "@/components/ui/table/table";
import s from "../../shared.module.css";
import { clientHost, type TrustedClient } from "./clients";

type Props = {
  clients: TrustedClient[];
  empty: string;
  onEdit?: (index: number) => void;
  onRemove?: (index: number) => void;
  /** A server refusal for one client, shown on its row. */
  problem?: { index: number; message: string };
};

export function ClientTable({ clients, empty, onEdit, onRemove, problem }: Props) {
  const editable = Boolean(onEdit && onRemove);
  return (
    <Table
      caption="Trusted clients"
      columns={["Client ID", "Label", ...(editable ? [{ label: "", stickyEnd: true }] : [])]}
      stack
      empty={clients.length === 0 ? <p className={s.muted}>{empty}</p> : undefined}
    >
      {clients.map((c, i) => {
        const host = clientHost(c.clientId);
        return (
          <Tr key={c.clientId}>
            <Td>
              <CellText wrap="anywhere" primary={<span className={s.mono}>{c.clientId}</span>} secondary={host ? `Vouched for by ${host}` : undefined} />
              {problem?.index === i && <span className={s.dangerText}>{problem.message}</span>}
            </Td>
            <Td>{c.label ? <CellText wrap="anywhere" primary={c.label} /> : <span className={s.muted}>—</span>}</Td>
            {editable && (
              <Td stickyEnd>
                <ActionMenu
                  label={`Actions for ${c.label || c.clientId}`}
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
