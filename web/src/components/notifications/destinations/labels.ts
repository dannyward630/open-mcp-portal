/* Words for webhook destinations: formats, health, delivery outcomes and the error classes of a failed send. */
import type { Schemas } from "../../../api/client";
import type { Destination } from "./api";

export const formatLabels: Record<Schemas["NotificationFormat"], string> = { json: "JSON (signed)", slack: "Slack" };

/** A failed send's class, in words (NotificationErrorClass). */
export const errorClassLabels: Record<Schemas["NotificationErrorClass"], string> = {
  not_allowed: "The address isn't allowed (blocked, not on the operator's webhook network list, or it redirected)",
  connect: "Couldn't connect",
  tls: "Certificate or TLS problem",
  timeout: "Timed out",
  auth: "Sign-in to the mail server was refused",
  rejected: "The destination refused it (4xx)",
  server_error: "The destination had an error (5xx)",
  address: "The address was refused",
  expired: "Still undelivered after 24 hours",
  internal: "Internal error",
};

export const errorClassText = (c: string | null | undefined) => (c ? (errorClassLabels[c as Schemas["NotificationErrorClass"]] ?? c) : "");

export const deliveryStateLabels: Record<Schemas["NotificationDelivery"]["state"], string> = { pending: "Waiting", sent: "Delivered", failed: "Failed", skipped: "Skipped" };
export const deliveryStateTone = { pending: "info", sent: "success", failed: "danger", skipped: "neutral" } as const;

/** Why a destination is stopped, or null while it isn't. */
export const stoppedByFailures = (d: Destination) => !d.enabled && d.disabledReason === "failures";

/** The graces offered when rotating a secret (hours of signing with both). */
export const graceChoices = [
  { value: "24", label: "24 hours (recommended)" },
  { value: "72", label: "72 hours" },
  { value: "6", label: "6 hours" },
  { value: "1", label: "1 hour" },
  { value: "0", label: "Stop the old secret now" },
] as const;
