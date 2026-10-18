/*
 * Add or change a webhook destination: a name, the format (JSON, signed, or Slack; fixed once made), the URL and the
 * events it gets. The URL is write-only: it's sealed on the server and never returned, only its host is shown
 * ("Currently https://hooks.example.com/…"); on edit, leaving it empty keeps it. A platform destination can also take
 * every team's events. Creating a JSON destination hands the signing secret to the caller, which shows it once.
 */
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import type { Schemas } from "../../../api/client";
import { V02ErrorAlert } from "../../errors";
import { FormDialog } from "../../form-dialog";
import { Checkbox, CheckboxGroup } from "@/components/ui/checkbox/checkbox";
import { Field } from "@/components/ui/field/field";
import { Input } from "@/components/ui/input/input";
import { RadioGroup } from "@/components/ui/radio-group/radio-group";
import { Switch } from "@/components/ui/switch/switch";
import { destinationEvents, eventDescriptions, eventLabels, type NotificationEvent } from "../events";
import { createDestination, type Destination, type DestinationScope, destinationsKey, updateDestination } from "./api";
import { formatLabels } from "./labels";

type Format = Schemas["NotificationFormat"];

type Props = {
  scope: DestinationScope;
  /** The destination to change; none to add one. */
  destination?: Destination;
  onClose: () => void;
  /** After creating: the destination and, for JSON, the secret to show once. */
  onCreated: (created: Schemas["NotificationDestinationCreated"]) => void;
};

const defaultEvents = (scope: DestinationScope): NotificationEvent[] => (scope.kind === "team" ? ["server.unhealthy", "server.recovered"] : ["publish.requested"]);

export function DestinationForm({ scope, destination, onClose, onCreated }: Props) {
  const qc = useQueryClient();
  const editing = Boolean(destination);
  const [name, setName] = useState(destination?.name ?? "");
  const [format, setFormat] = useState<Format>(destination?.format ?? "json");
  const [url, setUrl] = useState("");
  const [teamEvents, setTeamEvents] = useState(destination?.teamEvents ?? false);
  const [events, setEvents] = useState<NotificationEvent[]>(destination?.events ?? defaultEvents(scope));
  const [submitted, setSubmitted] = useState(false);
  const choices = destinationEvents(scope.kind, teamEvents);
  const chosen = events.filter((e) => choices.includes(e));
  const save = useMutation({
    mutationFn: async () => {
      const trimmed = url.trim();
      if (destination) return { updated: await updateDestination(scope, destination, { name: name.trim(), events: chosen, ...(scope.kind === "platform" ? { teamEvents } : {}), ...(trimmed ? { url: trimmed } : {}) }) };
      return { created: await createDestination(scope, { name: name.trim(), format, url: trimmed, events: chosen, teamEvents: scope.kind === "platform" && teamEvents, enabled: true }) };
    },
    onSuccess: (out) => {
      void qc.invalidateQueries({ queryKey: destinationsKey(scope) });
      if (out.created) onCreated(out.created);
      else onClose();
    },
  });
  const nameError = submitted && !name.trim() ? "Enter a name." : undefined;
  const urlError = submitted && !editing && !url.trim() ? "Enter the URL to send to." : undefined;
  const eventsError = submitted && chosen.length === 0 ? "Choose at least one event." : undefined;
  return (
    <FormDialog
      title={editing ? `Change ${destination?.name}` : "Add a webhook destination"}
      description={editing ? "The URL stays as it is unless you enter a new one." : "A webhook gets a message when one of the chosen events happens."}
      size="lg"
      submitLabel={editing ? "Save changes" : "Add destination"}
      busy={save.isPending}
      onClose={onClose}
      formProps={{ noValidate: true }}
      onSubmit={() => {
        setSubmitted(true);
        if (name.trim() && (editing || url.trim()) && chosen.length > 0) save.mutate();
      }}
    >
      <Field label="Name" description="Where it sends, for example “On-call channel”." error={nameError}>
        <Input maxLength={100} value={name} onChange={(e) => setName(e.target.value)} />
      </Field>
      <RadioGroup<Format>
        legend="Format"
        value={format}
        onValueChange={setFormat}
        disabled={editing}
        options={[
          { value: "json", label: formatLabels.json, description: "A JSON message signed with a secret you keep, for your own receiver." },
          { value: "slack", label: formatLabels.slack, description: "A line of text for a Slack-compatible incoming webhook. Not signed." },
        ]}
      />
      {editing && <p>The format can&apos;t be changed. Add another destination to use the other one.</p>}
      <Field
        label="URL"
        error={urlError}
        description={
          editing
            ? `Currently ${destination?.urlDisplay}. The URL is stored encrypted and never shown again: leave this empty to keep it, or enter a new one to replace it.`
            : "https to a public address. It is stored encrypted and never shown again; only its host is."
        }
      >
        <Input type="url" autoComplete="off" spellCheck={false} placeholder="https://hooks.example.com/…" value={url} onChange={(e) => setUrl(e.target.value)} />
      </Field>
      {scope.kind === "platform" && (
        <Switch
          label="Also receive every team's events"
          description="Server failures, capability changes, review decisions and sign-in lapses of all teams, besides reviews waiting and platform tokens expiring."
          checked={teamEvents}
          onCheckedChange={setTeamEvents}
        />
      )}
      <CheckboxGroup legend="Events" description="Messages are sent for these events only." error={eventsError} value={chosen} onValueChange={(v) => setEvents(v as NotificationEvent[])}>
        {choices.map((e) => (
          <Checkbox key={e} value={e} label={eventLabels[e]} description={eventDescriptions[e]} />
        ))}
      </CheckboxGroup>
      <V02ErrorAlert error={save.error} title={editing ? "Couldn't save the destination" : "Couldn't add the destination"} />
    </FormDialog>
  );
}
