# Phase 6: Operations (design)

Status: **designed, not built**. Decisions 31–38 in [decisions.md](decisions.md) are defaults the assistant accepted
under the instruction "continue until both are done". The contract is in [`../api/openapi.yaml`](../api/openapi.yaml)
(tags `limits`, `retention`, `usage`, and the call and payload operations under `gateways`/`admin`). Builds on decisions
11 and 14. Grounded's `internal/limits`, `internal/retention`, `internal/keyrotation` and `internal/observability` are
the reference, not dependencies. The Kubernetes slice is already built ([deploy.md](deploy.md)).

## Goal

Run the gateway for many teams without surprises. Callers get fair rate limits shared across replicas. Teams that
opt in can see what a call sent and got back, with secrets redacted. Data is deleted on a schedule. Teams and
platform admins see usage. Operators get Prometheus metrics, a key rotation procedure and CI.

## Data model (migration `00011_operations`, expand-only)

| Table / column | Contents |
|---|---|
| `limit_settings` (one row) | `items jsonb` (per key: default, ceiling), revision, updated_by/at |
| `capture_settings` (one row) | `patterns jsonb` (admin redaction regexes with notes), revision, updated_by/at |
| `retention_settings` (one row) | `periods jsonb` (days per kind), revision, updated_by/at, `last_run jsonb` |
| `teams` (+) | `limit_overrides jsonb`, `retention_overrides jsonb` (default `{}`; edits bump the team's revision, as the publishing override does) |
| `gateways` (+) | `rate_limit_per_minute integer NULL` (null: inherit); `capture_payloads` (exists since `00004`) becomes live |
| `mcp_servers` (+) | `capture_payloads boolean NOT NULL DEFAULT false` |
| `mcp_calls` (±) | outcome `rate_limited`. The append-only trigger allows `DELETE` only when `omg.calls_purge = 'on'` (the retention transaction), like `omg.audit_purge` |
| `mcp_call_payloads` | call_id (PK), occurred_at, gateway_team_id, server_team_id, by_gateway, by_server, arguments and result (sealed with `ENCRYPTION_KEY`, AAD `mcp_call_payload:<id>:<part>`), original sizes, truncated flags, redaction counts, captured_at |
| `rate_limit_counters` (UNLOGGED) | bucket text, window_start timestamptz, count int; PK (bucket, window_start) |
| `usage_hourly` | hour, gateway (id, slug, name, team), server (id, slug, name, team), capability_id, exposed_name, method, principal_kind, service_account_team_id, outcome → calls, latency_sum_ms, latency_buckets `int[]`, request/response bytes. `UNIQUE NULLS NOT DISTINCT` over the dimensions |
| `api_keys`, `service_account_keys`, `oauth_tokens` (+) | `pepper_id bytea NULL` (first 8 bytes of SHA-256 of the pepper that made the digest) |

## Roles

| Action | Who |
|---|---|
| Platform limit defaults and ceilings, capture settings (redaction patterns), retention periods | platform admins write; platform auditors read |
| Team limit and retention overrides | platform admins write; auditors read (admin routes); team members read the effective values (team routes) |
| A gateway's rate limit and capture switch | team owners/admins for team gateways; platform admins for platform gateways (admins still only switch team gateways on/off: `403 team_gateway`) |
| A server's capture switch | the server team's owners/admins (editors get `403 capture_role`; they may resend the current value) |
| Call detail | as the call lists: team owners/admins (team route), platform admins/auditors (admin route) |
| Captured payloads | owners/admins of the team whose switch captured it, through the team route; platform admins/auditors through the admin route. Every view is audited |
| Team usage | team owners, admins and editors; platform staff read any team. Admin usage: platform admins/auditors |

Platform roles never read payloads through team routes (SPEC §4); they use the admin route, which is audited.

## Rate limits (decision 31)

| Key | Counts | Built-in default | Team override |
|---|---|---|---|
| `person_per_minute` | every request by one person, any credential, any gateway | 600 | no |
| `credential_per_minute` | per personal API key or OAuth connection | 300 | no |
| `service_account_per_minute` | per service account, all its keys | 1200 | yes |
| `gateway_per_minute` | per gateway, all callers | unlimited | yes (default for the team's gateways) |

- Values are 1–1,000,000 per minute, or null (unlimited). Overridable keys have a **ceiling** (null: none). A team
  override may be above or below the default, but not above the ceiling. A per-gateway value is set on the gateway.
- **Effective:** service account = team override ?? default; gateway = gateway's value ?? (team gateway: team override)
  ?? default. Both are capped at the current ceiling. Lowering a ceiling keeps stored values; they are capped at runtime,
  and the admin view counts the capped teams and gateways.
- **What counts:** every authenticated `/mcp/{slug}` request that passes gateway resolution (initialize, ping,
  notifications, lists and calls). Requests answered 401 or 404 are not counted; front unauthenticated traffic with the
  ingress's own limits.
- **Algorithm:** fixed one-minute windows aligned to the database clock (`date_trunc('minute', now())`). One statement
  per request upserts every bucket that applies (`p:<user>`, `c:<key or grant>`, `s:<service account>`, `g:<gateway>`),
  in sorted order to avoid deadlocks, with `ON CONFLICT … DO UPDATE SET count = count + 1 RETURNING`. Refused requests
  count too. A minute boundary can let through up to twice a limit in a short burst; that is accepted. Rows older than
  two minutes are deleted every minute by any replica. The table is UNLOGGED (counters may be lost on a crash or
  failover). One gateway's row is a hot spot: fine to a few thousand requests per second per gateway; beyond that,
  a Valkey store behind the same interface (decision 11).
- **Over limit:** HTTP `429`, `Retry-After` (seconds to the window's end, at least 1), and a JSON-RPC error
  `-32029` "Rate limit exceeded (person); try again in 12 s" with `data: {reason: "rate_limited", limit: <key>,
  limitPerMinute, retryAfterSeconds}`. When several are exceeded, the most specific is named (credential, person,
  service account, gateway). Nothing is forwarded. Lists and calls are recorded with outcome `rate_limited` and error
  class = the key.
- **Headers** on every counted response: `RateLimit-Limit`, `RateLimit-Remaining` and `RateLimit-Reset` for the
  caller's own tightest limit (person, credential or service account). Gateway-wide counts are never shown to callers.
- **Other limiters move to the same store** (`internal/ratelimit` gets a PostgreSQL `Store`; the in-memory limiter stays
  for tests): OAuth authorize/token/revoke (60/min per address), register (10/min), connect start (20/min per person),
  sign-in (`LOGIN_ATTEMPTS_PER_MINUTE`) and `/v1` (`REQUESTS_PER_MINUTE`), keyed by a SHA-256 of the address, never the
  address itself. A store error answers 500/503, as any other database error does.
- **Audit:** `settings.limits_update`, `team.limits_update` (before/after), and gateway updates carry the limit.

## Payload capture (decision 32)

- **When:** a forwarded `tools/call`, `prompts/get` or `resources/read` is captured if its gateway's
  `capturePayloads` **or** its server's `capturePayloads` was on at the time of the call. `by_gateway` and `by_server`
  record which switch did it. Lists, initialize and anything refused before forwarding (denied, rate_limited,
  needs_authorization) are never captured.
- **What:** `arguments` = the call's `arguments` (or `{"uri": <exposed URI>}` for a read). `result` = what the client
  received (after the `_meta` scrub and URI rewrite), or the server's JSON-RPC error. When there is no result (timeout,
  unreachable, too large), only the arguments are kept. Never: headers, the identity JWT, provider tokens, client
  credentials, the server's static header.
- **Redaction, then truncation.** Parse the JSON and walk it:
  1. Members whose normalised key (lower case, without `-`/`_`) is a sensitive name (`password`, `passwd`, `secret`,
     `token`, `accesstoken`, `refreshtoken`, `idtoken`, `apikey`, `authorization`, `cookie`, `setcookie`,
     `clientsecret`, `privatekey`, `sessionid`) get the value `"[REDACTED]"`.
  2. In every string, matches of the built-in rules and the admin patterns become `[REDACTED]`. Built-in rules:
     `bearer` (`Bearer <token>`), `jwt` (`eyJ….….…`), `gateway_credentials` (`omcp_`, `omcp_sa_`, `omat_`, `omrt_`,
     `omac_` tokens), `github` (`gh[pousr]_…`, `github_pat_…`), `gitlab` (`glpat-…`), `slack` (`xox[abprs]-…`),
     `google` (`ya29.…`, `AIza…`), `aws_access_key` (`AKIA…`/`ASIA…`), `private_key_pem`, `url_userinfo`
     (`scheme://user:pass@`).
  3. `call_secrets`: exact values the gateway handled for this call (identity JWT, provider token, the server's
     static header value) are replaced wherever they appear, in case a server echoes them.
  4. `data` of image, audio and blob content items becomes `"[binary N bytes]"` (not counted as a redaction).
  Then each part is encoded and, if over **16 KiB**, cut at a UTF-8 boundary (`truncated: true`; the text is then no
  longer valid JSON). Counts per part are stored; never what was redacted.
- **Admin patterns:** at most 50 RE2 expressions (Go `regexp`), each ≤ 500 characters, not matching the empty string
  (`422 invalid_pattern` with `details.index` and `reason` `syntax`/`too_long`/`too_many`/`matches_empty`). They apply
  from the next capture on every replica (cached 30 s; the saving replica at once). `POST …/capture/test` shows what a
  sample would become with the given or saved patterns; the sample is never stored or logged.
- **Storage:** sealed with `ENCRYPTION_KEY` (they hold personal data); a capture failure never fails the call (logged,
  `omg_payload_capture_errors_total`). Written in the same transaction as the call record.
- **Visibility:** the team route answers when `{team}` owns the gateway and `by_gateway`, or owns the server and
  `by_server`, and the caller is its owner or admin; otherwise `404 payload_not_found`. `McpCall.captured` is true
  when the caller may open the payload (team views: under that team's switch; admin views: any payload).
  Responses are `Cache-Control: no-store`.
- **Audit:** `mcp_call.payload_view` (target `mcp_call` with the call ID; team = the gateway's team when captured by the
  gateway, else the server's team; metadata `via: team|admin`, `capturedBy`), so a team sees platform staff's views in
  its own audit log. `gateway.update`/`mcp_server.update` record `capturePayloads` before/after;
  `settings.capture_update` records the patterns (they are not secrets).
- **Transparency:** `MyGateway.capturesPayloads`, `GatewayCapability`/`GatewayOption.serverCapturesPayloads` and
  `CatalogServer.capturePayloads` tell callers and curators. Turning capture on is not a review trigger: the server
  already receives every argument and returns every result.

## Retention (decision 33)

| Kind | Default | Bounds (days) | Team override |
|---|---|---|---|
| `call_metadata` (`mcp_calls`) | 90 | 1–3650 | yes |
| `captured_payloads` | 14 | 1–90 | yes |
| `audit_log` | 400 | 30–3650 | no |
| `usage_rollups` (`usage_hourly`) | 400 | 90–3650 | no |

- A call's period is the **shortest** of the effective periods of the teams involved (gateway's team, server's team);
  with neither, the platform period. Payloads go when their own period passes or with their call.
- A leader-elected loop (advisory lock `omg.retention`, every `RETENTION_INTERVAL`, default 15m) deletes each kind in
  batches (`RETENTION_BATCH_SIZE`, default 1000 rows per transaction, at most 100 batches per kind per run): payloads,
  then calls (never newer than the usage rollup watermark), audit entries (`omg.audit_purge`), rollups. It also clears
  expired consent requests and rate counters. Idempotent; a failing kind doesn't stop the others.
- Each run that deleted something writes a system audit entry `retention.purge` with counts only; `last_run` (time,
  counts, errors per kind) is shown in Admin › Retention. Legal holds and dry runs are out of scope.
- **Audit:** `settings.retention_update`, `team.retention_update` (before/after). Shortening a period asks for
  confirmation in the UI.

## Usage (decision 34)

- **Rollups:** a leader-elected loop (`omg.usage-rollup`, every 5 minutes) recomputes the current and previous UTC hour
  from `mcp_calls` (delete and insert per hour, one transaction), so late commits are included and dashboards lag at
  most about five minutes (`rolledUpTo` in responses). Latency goes into fixed buckets (5, 10, 25, 50, 100, 250, 500,
  1000, 2500, 5000, 10000, 30000, 60000, 120000 ms, +Inf); p50/p95 are interpolated from merged buckets (accurate to a
  bucket).
- **API:** `GET /v1/teams/{team}/usage` and `GET /v1/admin/usage` with `from`/`to` (span ≤ 90 days, default the last 7
  days, aligned to hours), `groupBy` = `gateway` | `server` | `capability` | `team` | `principalKind` | `day` | `hour`
  (hour: span ≤ 7 days), filters `gateway`, `server`, `capability`, `principalKind`, `method`, and on the admin route
  `team`. `series=true` adds per-day buckets to each row of a non-time grouping. Rows are sorted by calls (time
  groupings: by time, with empty buckets); `limit` (default 50, at most 200) and `truncated`.
- **Team scope** is the call list's: through the team's gateways, to its servers, or by its service accounts. `team`
  groups by the gateway's team (platform gateways in one "Platform" row). No grouping by person.

## Metrics (decision 35)

`METRICS_ADDR` (for example `:9090`, empty = off) serves `/metrics` on its own listener, without authentication:
restrict it with NetworkPolicy. Dependency: `prometheus/client_golang` (in the Grounded set). Labels never carry user,
key, service account or request IDs.

| Metric | Labels |
|---|---|
| `omg_build_info` | version, commit |
| `omg_mcp_requests_total`, `omg_mcp_request_duration_seconds` | method (fixed list, else `other`), outcome |
| `omg_mcp_call_duration_seconds` (histogram, buckets as rollups) | gateway, server (slugs of existing objects, at most `METRICS_MAX_OBJECTS` = 200 each, else `other`; removed when deleted) |
| `omg_mcp_rate_limited_total` | limit (the four keys) |
| `omg_payload_captures_total`, `omg_payload_redactions_total`, `omg_payload_capture_errors_total` | rule (redactions only) |
| `omg_provider_token_refresh_total` | provider (slug), result (`ok`, `refused`, `unavailable`) |
| `omg_server_health_checks_total`, `omg_mcp_servers` (gauge) | result / health state |
| `omg_http_requests_total`, `omg_http_request_duration_seconds` | route (registered pattern), method, code class |
| `omg_retention_deleted_total`, `omg_retention_last_success_timestamp_seconds` | kind |
| `omg_usage_rollup_last_success_timestamp_seconds`, `omg_loop_leader` | loop |
| `omg_db_pool_*` (acquired, idle, total, max, acquire count and wait) | — |

Plus the Go and process collectors. Kubernetes: an optional `metrics` port in the example overlay, a NetworkPolicy
from the monitoring namespace, and an example `ServiceMonitor` (not in the base: it needs the CRD).

## Key rotation (decision 36)

- **`API_KEY_PEPPER_PREVIOUS`** (secret, 32 bytes base64, must differ from the current one; the example value is
  refused off loopback). Lookups digest with the current pepper, then the previous. A personal or service account key
  matched by the previous pepper is re-digested in the same request (digest and `pepper_id` updated; audited
  `api_key.pepper_rehash` / `service_account_key.pepper_rehash` by the system). OAuth access tokens and codes are only
  accepted (they expire within an hour); refresh tokens are accepted and replaced by their normal rotation.
  `pepper_id` NULL rows (made before this release) are stamped at start-up: with the current pepper's ID when no
  previous pepper is set, else with the previous one's.
- **`open-mcp-gateway rotate-keys [--dry-run] [--batch-size 100]`**: checks that every sealed value opens with
  `ENCRYPTION_KEY` or `ENCRYPTION_KEY_PREVIOUS` and stops, listing table and row ID, if any doesn't; then re-seals values
  still on the previous key in batches with row locks (AAD unchanged): server header values, signing keys' private keys,
  auth provider client secrets, connection tokens, PKCE verifiers, consent request IDs and captured payloads. It prints
  counts per column and the keys still on the previous pepper (kind, ID, name, owner or team, last use), and audits
  `platform.secrets_reencrypt` (per batch) and `platform.key_rotation` (counts only). Idempotent; safe while serving.
- **`doctor`** reports rows on the previous encryption key, undecryptable rows and keys on the previous pepper, and says
  when a `*_PREVIOUS` value can be removed. [deploy.md](deploy.md) "Key rotation" becomes the step-by-step procedure
  (set previous, roll, dry run, rotate, verify, remove `ENCRYPTION_KEY_PREVIOUS`, retire the pepper after 2–4 weeks).

## CI (decision 37)

`.github/workflows/ci.yml` (on push and pull request; `permissions: contents: read`; actions pinned by SHA;
concurrency cancels superseded runs of a branch):

- **lint:** `make lint` and `actionlint`.
- **generated:** `npm ci`, sqlc pinned, `make check-generated`.
- **go:** Postgres 17 service, `OMG_TEST_DATABASE_URL`, `OMG_TEST_REQUIRE_DB=1 go test -race -count=1 ./...` (a new
  `make test-ci` target: `test-integration` without `deps-up`).
- **web:** `npm ci`, typecheck, Vitest, `check:styling`, `check:size`, build.
- **k8s:** `make k8s-validate`. **docker:** `docker build` of both targets (no push).

There is no remote yet: the workflow is checked locally with `actionlint`; publishing images comes later.

## UI

- **Admin › Limits** (`/admin/limits`): the four keys with default and ceiling, capped counts; staged edit, Save with
  If-Match. **Admin › Retention** (`/admin/retention`): periods and the last run. **Admin › Payload capture**
  (`/admin/capture`): built-in rules, the pattern list and a tester.
- **Admin › Teams › team › Settings:** "Limits" and "Retention" sections (override or inherit, effective value).
- **Team › Settings › Limits & retention** (read-only); **Team › Usage** (`/teams/$team/usage`) and **Admin › Usage**
  (`/admin/usage`): range presets (24 h, 7 d, 30 d, 90 d), a calls-per-day chart by outcome, p50/p95, a grouped table.
- **Gateway › Settings:** "Rate limit" (inherit or a value, showing the effective one) and "Record call contents" with
  an explanation of who can read them and for how long. **Server › Settings:** "Record call contents" (owners/admins).
- **Calls:** a "Captured" badge; a call detail page (`…/calls/$callId`) with "Show contents" behind a notice that
  viewing is recorded. Badges in the picker, catalog and My gateways when contents are recorded.

## Tests that must exist

- Authz matrix for every new operation; editors can't change server capture; platform roles can't read payloads
  through team routes; a team that didn't capture gets `404 payload_not_found`.
- Rate limits: each key, effective-value rules (override, per gateway, ceiling capping), two handlers on one database
  share counters, 429 shape and headers, `rate_limited` records, nothing forwarded, OAuth/connect limits shared.
- Capture: gateway-only, server-only and both; lists and refused calls not captured; every built-in rule, admin
  patterns, exact-value redaction of the JWT and provider token echoed by the fake server, binary content, 16 KiB
  truncation at a UTF-8 boundary; a sentinel that no secret reaches `mcp_call_payloads`, logs or responses; views
  audited.
- Retention: shortest-team rule, payload and call purge in batches, the `mcp_calls` trigger still refuses other deletes,
  audit minimum, leader lock, idempotence. Usage: rollup equals a direct aggregate, late commits, percentiles, every
  grouping and filter, team scope, 90-day bound.
- Rotation: previous pepper accepted and re-digested, OAuth tokens accepted, `rotate-keys` dry run, re-seal of every
  column, undecryptable stop, idempotence. Metrics: no ID labels, bounded slugs. Vitest for every new screen.

## Build notes

Until the backend serves the new routes, `TestRoutesMatchOpenAPI` and the authz-matrix classification test fail and
`make check-generated` reports stale types; build the backend first, then `make generate`. New fields on existing
response schemas (`Gateway`, `McpServer`, `McpCall`, `MyGateway`, `CatalogServer`, `GatewayCapability`,
`GatewayOption`) are optional in the contract (absent from servers before Phase 6), so existing web fixtures stay valid;
a Phase 6 server always sends them.

## Not in this phase

Legal holds, retention dry runs and manual runs; per-tool or per-person limit overrides; quotas (per day/month); an
unauthenticated per-address limit on `/mcp`; tracing; dashboards and alert rules as code; payload search or export;
image publishing.
