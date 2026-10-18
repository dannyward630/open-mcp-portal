/*
 * Rotate a JSON destination's signing secret: asks first, with how long the old secret keeps signing too (so the
 * receiver can switch without dropping messages). The new secret goes to the caller to show once.
 */
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { friendly } from "../../../lib/error-wording";
import { AlertDialog } from "@/components/ui/dialog/dialog";
import { Select } from "@/components/ui/select/select";
import { type Destination, type DestinationScope, destinationsKey, rotateSecret } from "./api";
import { graceChoices } from "./labels";

type Props = { scope: DestinationScope; destination: Destination; onClose: () => void; onRotated: (d: Destination, secret: string) => void };

export function RotateDialog({ scope, destination, onClose, onRotated }: Props) {
  const qc = useQueryClient();
  const [grace, setGrace] = useState("24");
  const rotate = useMutation({
    mutationFn: () => rotateSecret(scope, destination, Number(grace)),
    onSuccess: (out) => {
      void qc.invalidateQueries({ queryKey: destinationsKey(scope) });
      onRotated(out.destination, out.secret);
    },
  });
  return (
    <AlertDialog
      open
      onOpenChange={(o) => !o && onClose()}
      tone="primary"
      title={`Rotate the signing secret of ${destination.name}?`}
      description="You get a new secret, shown once. Update your receiver with it. Until the old secret stops, every delivery carries both signatures."
      confirmLabel="Rotate secret"
      busy={rotate.isPending}
      error={friendly(rotate.error)}
      onConfirm={() => rotate.mutate()}
    >
      <Select label="Keep signing with the old secret for" items={graceChoices.map((g) => ({ value: g.value, label: g.label }))} value={grace} onValueChange={(v) => setGrace(v ?? "24")} />
    </AlertDialog>
  );
}
