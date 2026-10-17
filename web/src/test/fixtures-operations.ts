/* Phase 6 fixtures (limits, retention, payload capture, usage), typed from the generated contract. */
import type { Schemas } from "../api/client";
import { team } from "./fixtures";

export const platformLimit = (extra: Partial<Schemas["PlatformRateLimit"]> & Pick<Schemas["PlatformRateLimit"], "key">): Schemas["PlatformRateLimit"] => ({
  label: extra.key,
  description: "",
  teamOverridable: false,
  default: null,
  ceiling: null,
  builtInDefault: null,
  max: 1_000_000,
  cappedTeams: 0,
  cappedGateways: 0,
  ...extra,
});

export const limitSettings = (extra: Partial<Schemas["LimitSettings"]> = {}): Schemas["LimitSettings"] => ({
  items: [
    platformLimit({ key: "person_per_minute", label: "Per person", description: "Every request by one person.", default: 600, builtInDefault: 600 }),
    platformLimit({ key: "credential_per_minute", label: "Per API key or connected app", description: "Each personal key or OAuth connection.", default: 300, builtInDefault: 300 }),
    platformLimit({
      key: "service_account_per_minute",
      label: "Per service account",
      description: "All of a service account's keys.",
      teamOverridable: true,
      default: 1200,
      builtInDefault: 1200,
      ceiling: 5000,
    }),
    platformLimit({ key: "gateway_per_minute", label: "Per gateway", description: "All callers of one gateway.", teamOverridable: true, default: null, ceiling: null }),
  ],
  revision: 4,
  updatedAt: "2026-10-15T10:00:00Z",
  ...extra,
});

export const teamLimits = (extra: Partial<Schemas["TeamLimits"]> = {}): Schemas["TeamLimits"] => ({
  teamId: team.id,
  teamSlug: team.slug,
  revision: team.revision,
  items: [
    { key: "person_per_minute", label: "Per person", description: "Every request by one person.", teamOverridable: false, override: null, default: 600, ceiling: null, effective: 600 },
    { key: "credential_per_minute", label: "Per API key or connected app", description: "Each key.", teamOverridable: false, override: null, default: 300, ceiling: null, effective: 300 },
    { key: "service_account_per_minute", label: "Per service account", description: "All its keys.", teamOverridable: true, override: null, default: 1200, ceiling: 5000, effective: 1200 },
    { key: "gateway_per_minute", label: "Per gateway", description: "All callers of a gateway.", teamOverridable: true, override: 2000, default: null, ceiling: 3000, effective: 2000 },
  ],
  ...extra,
});

export const retentionSettings = (extra: Partial<Schemas["RetentionSettings"]> = {}): Schemas["RetentionSettings"] => ({
  periods: [
    { kind: "call_metadata", days: 90, builtInDays: 90, minDays: 1, maxDays: 3650, teamOverridable: true },
    { kind: "captured_payloads", days: 14, builtInDays: 14, minDays: 1, maxDays: 90, teamOverridable: true },
    { kind: "audit_log", days: 400, builtInDays: 400, minDays: 30, maxDays: 3650, teamOverridable: false },
    { kind: "usage_rollups", days: 400, builtInDays: 400, minDays: 90, maxDays: 3650, teamOverridable: false },
  ],
  revision: 2,
  updatedAt: "2026-10-15T10:00:00Z",
  lastRun: {
    startedAt: "2026-10-16T07:45:00Z",
    finishedAt: "2026-10-16T07:45:03Z",
    deleted: { call_metadata: 1250, captured_payloads: 40, audit_log: 0, usage_rollups: 0 },
    errors: {},
  },
  ...extra,
});

export const teamRetention = (extra: Partial<Schemas["TeamRetention"]> = {}): Schemas["TeamRetention"] => ({
  teamId: team.id,
  teamSlug: team.slug,
  revision: team.revision,
  periods: [
    { kind: "call_metadata", override: null, default: 90, effective: 90, minDays: 1, maxDays: 3650 },
    { kind: "captured_payloads", override: 30, default: 14, effective: 30, minDays: 1, maxDays: 90 },
  ],
  ...extra,
});

export const captureSettings = (extra: Partial<Schemas["CaptureSettings"]> = {}): Schemas["CaptureSettings"] => ({
  builtInRules: [
    { name: "bearer", description: "Bearer tokens" },
    { name: "github", description: "GitHub tokens" },
  ],
  sensitiveKeys: ["password", "token", "apikey"],
  patterns: [{ pattern: "EMP-[0-9]{6}", note: "Employee numbers" }],
  maxPartBytes: 16384,
  revision: 5,
  updatedAt: "2026-10-15T10:00:00Z",
  ...extra,
});

export const callPayload = (extra: Partial<Schemas["CallPayload"]> = {}): Schemas["CallPayload"] => ({
  callId: 501,
  capturedAt: "2026-10-16T07:00:00Z",
  expiresAt: "2026-10-30T07:00:00Z",
  capturedBy: ["gateway"],
  arguments: { content: '{"service":"<img src=x onerror=alert(1)>","token":"[REDACTED]"}', bytes: 64, truncated: false, redactions: 1 },
  result: { content: '{"content":[{"type":"text","text":"<b>up</b>', bytes: 40000, truncated: true, redactions: 0 },
  redactions: 1,
  truncated: true,
  ...extra,
});

export const metrics = (extra: Partial<Schemas["UsageMetrics"]> = {}): Schemas["UsageMetrics"] => ({
  calls: 0,
  errors: 0,
  outcomes: {},
  p50LatencyMs: null,
  p95LatencyMs: null,
  requestBytes: 0,
  responseBytes: 0,
  ...extra,
});

export const usageReport = (extra: Partial<Schemas["UsageReport"]> = {}): Schemas["UsageReport"] => ({
  from: "2026-10-09T08:00:00Z",
  to: "2026-10-16T08:00:00Z",
  groupBy: "day",
  rolledUpTo: "2026-10-16T07:55:00Z",
  totals: metrics({ calls: 120, errors: 6, outcomes: { ok: 114, tool_error: 4, rate_limited: 2 }, p50LatencyMs: 80, p95LatencyMs: 900, requestBytes: 20480, responseBytes: 3 * 1024 * 1024 }),
  rows: [
    { key: "2026-10-15T00:00:00Z", label: "2026-10-15", start: "2026-10-15T00:00:00Z", metrics: metrics({ calls: 70, errors: 2, outcomes: { ok: 68, tool_error: 2 }, p50LatencyMs: 70, p95LatencyMs: 800 }) },
    { key: "2026-10-16T00:00:00Z", label: "2026-10-16", start: "2026-10-16T00:00:00Z", metrics: metrics({ calls: 50, errors: 4, outcomes: { ok: 46, tool_error: 2, rate_limited: 2 }, p50LatencyMs: 90, p95LatencyMs: 1000 }) },
  ],
  truncated: false,
  ...extra,
});
