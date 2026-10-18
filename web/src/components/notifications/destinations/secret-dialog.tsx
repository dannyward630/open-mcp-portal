/*
 * A webhook's signing secret, shown once: after creating a JSON destination and after rotating the secret. It's gone
 * when the dialog closes. Slack destinations aren't signed and never show one.
 */
import { Alert } from "@/components/ui/alert/alert";
import { Button } from "@/components/ui/button/button";
import { CopyField } from "@/components/ui/copy-field/copy-field";
import { Dialog } from "@/components/ui/dialog/dialog";
import { Stack } from "@/components/ui/layout/layout";
import { formatDate, toDate } from "../../../lib/bitop-format";

type Props = {
  title: string;
  secret: string;
  /** When rotating: until when the previous secret signs too. */
  previousUntil?: string | null;
  onClose: () => void;
};

export function SecretDialog({ title, secret, previousUntil, onClose }: Props) {
  const until = toDate(previousUntil);
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={title} size="lg" footer={<Button onClick={onClose}>Done</Button>}>
      <Stack gap={5}>
        <Alert tone="warning" title="Copy this signing secret now.">
          It won&apos;t be shown again. Store it where your receiver can read it. If you lose it, rotate the secret to get a new one.
        </Alert>
        <CopyField
          label="Signing secret"
          name="signing secret"
          value={secret}
          description={'Each delivery carries X-OMP-Timestamp and X-OMP-Signature: v1=<hex>, the HMAC-SHA256 of the timestamp, a dot and the raw body, keyed with this secret. Reject timestamps older than 5 minutes.'}
        />
        {until && (
          <Alert tone="info" title="Both secrets sign for now">
            Until {formatDate(until, { style: "long" })} every delivery carries a signature for the new secret and one for the old, so you can switch without dropping messages.
          </Alert>
        )}
      </Stack>
    </Dialog>
  );
}
