/*
 * A gateway's Settings: its rate limit (inherit the team's or platform's
 * default, or its own value up to the ceiling; what applies now and, for an
 * unsaved change, what will after saving) and the
 * "Record call contents" switch with who can read the recordings and for how
 * long. Part of the settings form; turning recording on asks first
 * (settings-tab.tsx).
 */
import { useQuery } from "@tanstack/react-query";
import { Field } from "@/components/ui/field/field";
import { NativeSelect } from "@/components/ui/input/input";
import { NumberInput } from "@/components/ui/number-input/number-input";
import { Switch } from "@/components/ui/switch/switch";
import { limitSettingsQuery, parseRate, rateProblem, rateText, teamLimitsQuery } from "../../lib/limits";
import { useCurrentUser } from "../../session";
import { captureWhat, captureWho, useCaptureRetention } from "../capture";
import { SettingsSection } from "../templates/settings-page";
import type { Gateway } from "./labels";
import g from "./gateways.module.css";

export type RateMode = "inherit" | "custom";

/** What a gateway without its own value gets (the team's override or the platform default, capped), and the ceiling. */
export function useGatewayLimit(gateway: Gateway): { inherited?: number | null; ceiling?: number | null; from: string } {
  const me = useCurrentUser();
  const staff = me.capabilities.platformAdmin || me.capabilities.platformAuditor;
  const team = gateway.teamSlug;
  const teamLimits = useQuery({ ...teamLimitsQuery(team ?? ""), enabled: Boolean(team), staleTime: 60_000 });
  const platform = useQuery({ ...limitSettingsQuery(), enabled: !team && staff, staleTime: 60_000 });
  if (team) {
    const l = teamLimits.data?.items.find((x) => x.key === "gateway_per_minute");
    return { inherited: l?.effective, ceiling: l?.ceiling, from: l?.override !== null && l?.override !== undefined ? "the team's default" : "the platform default" };
  }
  const p = platform.data?.items.find((x) => x.key === "gateway_per_minute");
  const inherited = p ? (p.ceiling === null ? p.default : p.default === null ? p.ceiling : Math.min(p.default, p.ceiling)) : undefined;
  return { inherited, ceiling: p?.ceiling, from: "the platform default" };
}

type RateProps = {
  gateway: Gateway;
  mode: RateMode;
  value: string;
  error?: string;
  disabled: boolean;
  onChange: (mode: RateMode, value: string) => void;
};

/**
 * "Applies now: 2,000 per minute.", plus what an unsaved change will apply: "After saving: 1,500 per minute.", or,
 * while the staged value is invalid or the inherited one isn't known yet, that the change applies after saving.
 */
export function appliesText(now: number | null | undefined, staged: { own: number | null; effective?: number | null } | undefined, savedOwn: number | null) {
  if (now === undefined) return undefined;
  const current = `Applies now: ${rateText(now)}.`;
  if (!staged) return `${current} Your change applies after saving.`;
  if (staged.own === savedOwn) return current;
  return staged.effective === undefined ? `${current} Your change applies after saving.` : `${current} After saving: ${rateText(staged.effective)}.`;
}

export function RateLimitSection({ gateway, mode, value, error, disabled, onChange }: RateProps) {
  const { inherited, ceiling, from } = useGatewayLimit(gateway);
  const known = inherited !== undefined;
  const now = gateway.effectiveRateLimitPerMinute;
  const savedOwn = gateway.rateLimitPerMinute ?? null;
  // The staged value: inherit, or a valid number (a custom value is never empty: empty is "Enter a number").
  const own = mode === "inherit" ? null : parseRate(value);
  const valid = mode === "inherit" || (own !== null && !rateProblem(value, ceiling));
  const staged = valid ? { own, effective: own === null ? inherited : own } : mode === "custom" && own !== null && own === savedOwn ? { own } : undefined;
  const applies = appliesText(now, staged, savedOwn);
  return (
    <SettingsSection
      title="Rate limit"
      description="Requests per minute from all callers together. Over it, callers are told to try again shortly (HTTP 429); each person, key and service account also has its own limit."
    >
      <Field label="Limit" description={known ? `Inherit: ${rateText(inherited)}, ${from}.` : undefined}>
        <NativeSelect value={mode} disabled={disabled} onChange={(e) => onChange(e.target.value as RateMode, value)}>
          <option value="inherit">{known ? `Inherit (${rateText(inherited)})` : "Inherit"}</option>
          <option value="custom">This gateway&apos;s own limit</option>
        </NativeSelect>
      </Field>
      {mode === "custom" && (
        <Field
          label="Requests per minute"
          error={error}
          description={ceiling !== null && ceiling !== undefined ? `At most ${ceiling.toLocaleString()} (the platform's ceiling).` : "From 1 to 1,000,000."}
        >
          <NumberInput maximumFractionDigits={0} value={value} disabled={disabled} unit="per minute" onValueChange={(v) => onChange(mode, v)} />
        </Field>
      )}
      {applies && (
        <p className={g.note} aria-live="polite">
          {applies}
        </p>
      )}
    </SettingsSection>
  );
}

type CaptureProps = { gateway: Gateway; checked: boolean; disabled: boolean; onChange: (v: boolean) => void };

export function CaptureSection({ gateway, checked, disabled, onChange }: CaptureProps) {
  const kept = useCaptureRetention(gateway.teamSlug, gateway.ownerKind === "platform");
  const who =
    gateway.ownerKind === "team"
      ? `Readable by ${gateway.teamName ?? "the team"}'s owners and admins and by platform admins and auditors. Every view is recorded in the audit log.`
      : captureWho;
  return (
    <SettingsSection
      title="Record call contents"
      description="Off by default: call records hold only who called what, when and how it went. Callers see that this gateway records what they send."
    >
      <Switch
        label="Record call contents"
        description={[captureWhat, who, kept].filter(Boolean).join(" ")}
        disabled={disabled}
        checked={checked}
        onCheckedChange={onChange}
      />
    </SettingsSection>
  );
}
