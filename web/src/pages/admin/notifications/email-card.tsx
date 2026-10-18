/*
 * Admin › Notifications › Email: whether the operator configured SMTP (SMTP_HOST and friends in the deployment: the
 * portal never shows the user name or password), the From address, host, port, TLS mode, and the last 24 hours.
 * Platform admins can send a test email to their own address; the result (ok, or why not) shows in the dialog.
 */
import { useMutation } from "@tanstack/react-query";
import { Send } from "lucide-react";
import { useState } from "react";
import { api, type Schemas, unwrap } from "@/api/client";
import { V02ErrorAlert } from "@/components/errors";
import { errorClassText } from "@/components/notifications/destinations/labels";
import { Alert } from "@/components/ui/alert/alert";
import { StatusBadge } from "@/components/ui/badge/badge";
import { Button } from "@/components/ui/button/button";
import { Card } from "@/components/ui/card/card";
import { DescriptionList } from "@/components/ui/description-list/description-list";
import { Dialog, DialogClose } from "@/components/ui/dialog/dialog";
import { Stack } from "@/components/ui/layout/layout";

type Email = Schemas["NotificationEmailStatus"];

const tlsLabels = { starttls: "STARTTLS (required)", tls: "TLS from the start", none: "None (unencrypted)" } as const;

export function EmailCard({ email, canTest }: { email: Email; canTest: boolean }) {
  const [testing, setTesting] = useState(false);
  return (
    <Card
      title="Email"
      description="Sent by the portal's own mail server settings, as plain text, one message per person."
      actions={
        canTest && (
          <Button size="sm" variant="secondary" disabled={!email.configured} title={email.configured ? undefined : "Email isn't set up."} onClick={() => setTesting(true)}>
            <Send aria-hidden /> Send test email
          </Button>
        )
      }
    >
      <Stack gap={4}>
        {email.configured ? (
          <>
            <StatusBadge tone="success">Email is set up</StatusBadge>
            <DescriptionList
              size="sm"
              items={[
                { label: "From", value: email.from },
                { label: "Server", value: email.host ? `${email.host}${email.port ? `:${email.port}` : ""}` : "" },
                { label: "Encryption", value: email.tls ? tlsLabels[email.tls] : "" },
                { label: "Signs in", value: email.authenticated ? "Yes (user name and password are set in the deployment)" : "No" },
                { label: "Last 24 hours", value: `${email.sentLast24h.toLocaleString()} sent, ${email.failedLast24h.toLocaleString()} failed` },
              ]}
            />
            {email.failedLast24h > 0 && (
              <Alert tone="warning" title={`${email.failedLast24h.toLocaleString()} emails failed in the last 24 hours`}>
                Send yourself a test email to see why. Most failures are a mail server that refuses the login, the sender address or the certificate.
              </Alert>
            )}
          </>
        ) : (
          <Alert tone="info" title="Email isn't set up">
            The portal sends no email. The in-app inbox and webhooks still work. To turn email on, set SMTP_HOST, SMTP_FROM and the related SMTP settings in the deployment, then restart the portal. People see
            their Email switches turned off until then.
          </Alert>
        )}
      </Stack>
      {testing && <TestEmailDialog onClose={() => setTesting(false)} />}
    </Card>
  );
}

function TestEmailDialog({ onClose }: { onClose: () => void }) {
  const send = useMutation({ mutationFn: async () => unwrap(await api.POST("/v1/admin/settings/notifications/test-email")) });
  const result = send.data;
  return (
    <Dialog
      open
      onOpenChange={(o) => !o && onClose()}
      title="Send a test email"
      description="Sends a short message to the address on your own profile, never to any other address, through the configured mail server. 5 tests an hour."
      footer={
        <>
          <DialogClose>{result ? "Close" : "Cancel"}</DialogClose>
          <Button loading={send.isPending} onClick={() => send.mutate()}>
            <Send aria-hidden /> {result ? "Send again" : "Send test email"}
          </Button>
        </>
      }
    >
      <Stack gap={4}>
        {result && (
          <Alert tone={result.ok ? "success" : "danger"} title={result.ok ? "The mail server accepted the test email" : "The test email failed"}>
            <p>{result.message}</p>
            <p>{result.errorClass ? `${errorClassText(result.errorClass)}. ` : ""}{result.durationMs} ms.</p>
          </Alert>
        )}
        <V02ErrorAlert error={send.error} title="Couldn't send the test email" />
      </Stack>
    </Dialog>
  );
}
