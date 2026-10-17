/*
 * Recording call contents (payload capture, decision 32): the badge shown wherever a gateway or server records
 * what callers send, and the words that explain who can read the recordings and for how long.
 */
import { useQuery } from "@tanstack/react-query";
import { Badge } from "@/components/ui/badge/badge";
import { daysText, retentionSettingsQuery, teamRetentionQuery } from "../lib/retention";

/** What a recording holds, in one sentence. */
export const captureWhat =
  "The arguments and results of tool calls, prompts and resource reads, with secret-looking values (tokens, passwords, keys) replaced by [REDACTED] and each part cut to 16 KiB.";

/** Who reads recordings: the owners and admins of the team whose switch recorded it, and platform staff. */
export const captureWho =
  "Readable by the team's owners and admins and by platform admins and auditors. Every view is recorded in the audit log.";

/** Told to callers before they use something that records. */
export const capturedNotice = "Its owners can read what you send through it and what you get back (secrets are removed first). Avoid sending anything you wouldn't share with them.";

/** "Records call contents", with what that means as its tooltip. */
export function CaptureBadge({ size = "sm", label = "Records call contents" }: { size?: "sm" | "md"; label?: string }) {
  return (
    <Badge tone="warning" size={size} title={`${captureWhat} ${captureWho}`}>
      {label}
    </Badge>
  );
}

/** Said instead of the period while it loads (or when it can't be read). */
export const keptFallback = "Kept for the retention period set for recorded call contents, then deleted.";

/**
 * "Kept for 14 days" for a team's recordings (the team's effective period), or the platform's in Admin; undefined
 * until it's known (the sentence is then left out).
 */
export function useCaptureRetention(team: string | undefined, admin: boolean): string | undefined {
  const teamPeriod = useQuery({ ...teamRetentionQuery(team ?? ""), enabled: Boolean(team), staleTime: 60_000 });
  const platform = useQuery({ ...retentionSettingsQuery(), enabled: !team && admin, staleTime: 60_000 });
  const days = team
    ? teamPeriod.data?.periods.find((p) => p.kind === "captured_payloads")?.effective
    : platform.data?.periods.find((p) => p.kind === "captured_payloads")?.days;
  return days ? `Kept for ${daysText(days)}, then deleted.` : undefined;
}
