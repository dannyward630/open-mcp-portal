/*
 * Account › Notifications (/settings/notifications), from the account menu: for each event the person can receive,
 * whether it reaches them in the portal (the bell and Inbox) and by email. Switches save at once. Events appear only
 * for roles that receive them. The Email column is off, with the reason, while the operator hasn't set up email
 * (emailAvailable): choices are kept for when it is. Webhooks are a team's or the platform's, not personal.
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap, type Schemas } from "../../api/client";
import { preferencesQuery } from "../../api/notifications";
import { eventDescriptions, eventLabels, eventRecipients } from "../../components/notifications/events";
import { PageSkeleton } from "../../components/page-skeleton";
import { terms } from "../../lib/terms";
import { Alert, ErrorAlert } from "@/components/ui/alert/alert";
import { Card } from "@/components/ui/card/card";
import { Stack } from "@/components/ui/layout/layout";
import { PageHeader } from "@/components/ui/page-header/page-header";
import { Switch } from "@/components/ui/switch/switch";
import { Table, Td, Tr } from "@/components/ui/table/table";
import { VisuallyHidden } from "@/components/ui/visually-hidden/visually-hidden";
import s from "../shared.module.css";

type Prefs = Schemas["NotificationPreferences"];
type Channel = "inApp" | "email";

const channelNames: Record<Channel, string> = { inApp: "in the portal", email: "by email" };

export function NotificationSettingsPage() {
  const qc = useQueryClient();
  const prefs = useQuery(preferencesQuery());
  const save = useMutation({
    mutationFn: async (change: Schemas["NotificationPreferenceChange"]) =>
      unwrap(await api.PUT("/v1/me/notification-preferences", { body: { items: [change] } })),
    // The switch moves at once; the server's answer (or a refetch after a failure) has the last word.
    onMutate: async (change) => {
      await qc.cancelQueries({ queryKey: preferencesQuery().queryKey });
      qc.setQueryData<Prefs>(preferencesQuery().queryKey, (p) =>
        p ? { ...p, items: p.items.map((i) => (i.event === change.event ? { ...i, ...change, custom: true } : i)) } : p,
      );
    },
    onSuccess: (next) => qc.setQueryData(preferencesQuery().queryKey, next),
    onError: () => void qc.invalidateQueries({ queryKey: preferencesQuery().queryKey }),
  });
  const data = prefs.data;
  return (
    <Stack gap={6} className={s.page}>
      <PageHeader title={terms.notifications} description="Choose which events reach you in the portal and by email. The bell and Inbox show what you keep on." />
      {prefs.isLoading ? (
        <PageSkeleton />
      ) : data ? (
        <>
          <EmailNote data={data} />
          <Card flush>
            <Table
              caption="Notification preferences"
              columns={["Event", "In the portal", "Email"]}
              empty={data.items.length === 0 ? <p className={s.muted}>No events reach you.</p> : undefined}
              stack
            >
              {data.items.map((item) => (
                <Tr key={item.event}>
                  <Td>
                    <span className={s.primary}>{eventLabels[item.event]}</span>
                    <span className={s.secondary}>{eventDescriptions[item.event]}</span>
                    <span className={s.secondary}>Sent to: {eventRecipients[item.event]}.</span>
                  </Td>
                  {(["inApp", "email"] as const).map((channel) => (
                    <Td key={channel} nowrap>
                      <Switch
                        label={<VisuallyHidden>{`${eventLabels[item.event]} ${channelNames[channel]}`}</VisuallyHidden>}
                        checked={item[channel]}
                        disabled={channel === "email" && !data.emailAvailable}
                        onCheckedChange={(on) => save.mutate({ event: item.event, [channel]: on })}
                      />
                    </Td>
                  ))}
                </Tr>
              ))}
            </Table>
          </Card>
          <ErrorAlert error={save.error} title="Couldn't save that choice" />
        </>
      ) : (
        <ErrorAlert error={prefs.error} title="Couldn't load your notification settings" onRetry={() => void prefs.refetch()} />
      )}
    </Stack>
  );
}

/** Where email goes, or why the Email column is off. */
function EmailNote({ data }: { data: Prefs }) {
  if (!data.emailAvailable) {
    return (
      <Alert tone="info" title="Email isn't set up on this portal">
        Nothing is sent by email, so the Email switches are off. Your choices are kept for when an administrator sets email up.
      </Alert>
    );
  }
  return (
    <Alert tone="info">
      Email goes to <strong>{data.emailAddress}</strong>, the address on your profile. At most 30 emails an hour; the portal always has the rest.
    </Alert>
  );
}
