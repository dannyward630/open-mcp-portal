# Notifications (design)

Status: **designed for v0.2 (roadmap B), not built.** Decision 56. The contract is in [`../api/openapi.yaml`](../api/openapi.yaml) (tag `notifications`; payload
schemas `NotificationPayload` and `*Data`). Migration `00014_notifications.sql`. Reference, not copied: the sister project Open Model Gateway's
`docs/alerts.md` (visibility follows live authority, per-person read state, outcome categories, at-most-one-sender locking). The difference: OMG evaluates
rules; here the portal's own changes and loops *emit events*, and a leader-elected dispatcher delivers them.

## Goal

Tell the right people, in the portal or elsewhere, when something needs them: a server is down or changed, a review is waiting or decided, a sign-in lapsed, a key is about to expire.
Three channels: **in-app inbox** (bell), **email** (the operator's SMTP) and **webhooks** (signed JSON, or Slack-compatible). Notifications never block or change anything they report on.

## Configuration (operator, `internal/config`)

| Setting | Default | Notes |
|---|---|---|
| `SMTP_HOST` | empty: **email off** | Host name or address, no scheme |
| `SMTP_PORT` | 587 (`starttls`), 465 (`tls`), 25 (`none`) | 1-65535 |
| `SMTP_USERNAME`, `SMTP_PASSWORD` | empty | Both or neither. The password is a secret: `SMTP_PASSWORD_FILE` works; never logged, shown or returned |
| `SMTP_FROM` | required with `SMTP_HOST` | One address, `portal@example.org` or `Open MCP Portal <portal@example.org>`; no control characters |
| `SMTP_TLS` | `starttls` | `starttls` (required: a server that doesn't offer it fails, never a downgrade), `tls` (implicit), `none` |
| `NOTIFICATION_INTERVAL` | `15s` | Dispatcher tick, 5s-10m |

Validation (`Validate("serve")`): any `SMTP_*` without `SMTP_HOST` is an error (a half configuration must not silently mean "off"); `SMTP_FROM` missing or invalid; username without password or the reverse; bad port or mode.
Safety (`SafetyErrors`, unless `APP_URL` is loopback): `SMTP_TLS=none` **with credentials** is refused unless `SMTP_HOST` is loopback (plain-text password); `none` without credentials is allowed (an in-cluster relay) and `doctor` says "info: unencrypted".
Certificates are always verified (system roots, SNI = host); there is no insecure switch. SMTP is operator configuration, trusted, so it **does not** go through the SSRF guard; `doctor` reports it (group `notifications`, check `email`):
off = info; configured = connect, EHLO, TLS, AUTH and QUIT within 5 s (no message sent), `ok` with host, port, mode and "authenticated", or `fail` with the error class. The Kubernetes base's egress must be extended for the SMTP port (and webhook ports other than 443) in the overlay; [deploy.md](deploy.md) gets the rows when this is built.

## Events, recipients and dedup

Recipients are resolved **at fan-out from live roles** (like OMG), never stored: suspended people and archived teams get nothing, and the person who caused a manual event is skipped. Stable names:

| Event | Fires when | Dedup key | In-app and email to | Webhooks |
|---|---|---|---|---|
| `server.unhealthy` | A stored check failed and the last **3** checks (scheduled or manual) all failed; once per incident (`status_since`), then a reminder every 24 h while it still fails | `server.unhealthy:<server>:<since>:<n>` (n = 0, then whole days since) | team owners, admins, editors | team, platform* |
| `server.recovered` | The first ok check after an incident that had an unhealthy event (looked up in `notification_events`; reminders keep it inside the 14 days) | `server.recovered:<server>:<since>` | owners, admins, editors | team, platform* |
| `capability.changed` | A refresh leaves a **published** server with changed or new unapproved capabilities not all held before (they are hidden from the catalog) | `capability.changed:<server>:<hash of the held set>` | owners, admins, editors | team, platform* |
| `publish.requested` | A server enters `pending`: first request, or sent back (`url_changed`, `upstream_auth_changed`, `requirement_changed`) | `publish.requested:<server>:<requested_at>` | **platform admins** | platform |
| `publish.decided` | A platform admin approves or rejects | `publish.decided:<server>:<reviewed_at>` | team owners, admins | team, platform* |
| `connection.needs_reconsent` | A person's connection turns `needs_reconsent` (not a server's setup sign-in) | `…:<connection>:<marked_at>` | **that person** | never |
| `upstream_signin.lapsed` | The setup sign-in of an `oauth` server turns `needs_reconsent` | `…:<server>:<marked_at>` | owners, admins, editors | team, platform* |
| `key.expiring` | The hourly scan finds an active key or token, **made for more than 7 days**, expiring within 7 days | `key.expiring:<kind>:<id>` | personal key: the person. Service-account key, publisher token: team owners and admins. Platform-admin token: platform admins | team (service-account keys, publisher tokens), platform (platform tokens; the others with `teamEvents`) |

\* A platform destination gets the non-personal team events of every team only when its `teamEvents` switch is on. `notification.delivery_failed` is **not** an event: failures show in the UI
(delivery log, destination health, Admin › Overview). An event is emitted by `notify.Emit(ctx, q, event)` inside the transaction of the change, under a savepoint, `ON CONFLICT (dedup_key) DO NOTHING`;
a failure is logged and counted (`omp_notification_emit_errors_total`) and never fails the change. Loops that detect (health, expiry scan) are idempotent through the same keys.

**Preferences** are per person, event and channel (`in_app`, `email`), listed only for events the person can receive; defaults: in-app on, email on. Off for a channel means no inbox row or no email row.
Email also needs SMTP (`emailAvailable`). **Destinations** are webhooks: team destinations are managed by the team's owners and admins, platform destinations by platform admins; each chooses its events.

## Delivery

`internal/notify`, one loop, advisory lock `omp.notifications` (leader election like the health loop), every `NOTIFICATION_INTERVAL`:

1. hourly: scan for expiring keys and tokens, emit `key.expiring`;
2. **fan out** up to 200 events with `fanned_out_at IS NULL`, one transaction each: resolve recipients, apply preferences, insert inbox rows, email and webhook outbox rows (idempotent: unique `(user_id, event_id)` and outbox `dedup_key`), set `fanned_out_at`;
3. **send** up to 50 due outbox rows, 8 at a time (`FOR UPDATE SKIP LOCKED`): attempt 1 now, then after 1 m, 5 m, 30 m, 2 h, 6 h (6 attempts), then `failed`; a row undelivered 24 h after creation becomes `failed` / `expired`; permanent errors (webhook 4xx except 408 and 429, SMTP 5xx) fail at once; a destination with 20 failed attempts in a row is **stopped** (`enabled` false, `disabledReason: failures`, audited, shown); deliveries to disabled or deleted destinations are `skipped`;
4. hourly housekeeping (below). Delivery is **at least once**: a crash between sending and recording repeats it, so receivers de-duplicate on `X-OMP-Event-Id`.

**Webhooks.** `POST`, 10 s, redirects never followed, proxies ignored, TLS verified, the response body is never kept. The URL is checked on save and on **every** attempt with the guarded dialer of MCP servers: public addresses over https,
internal ones only by the internal-network allowlist (decision 20, `allowHttp` per entry), loopback, link-local and metadata never, DNS pinned, rebinding refused; `422 url_not_allowed` with the same reasons.
Headers: `Content-Type: application/json`, `User-Agent: open-mcp-portal/<version>`, `X-OMP-Event`, `X-OMP-Event-Id`, `X-OMP-Delivery` (outbox id), `X-OMP-Attempt`, and for `json`:
`X-OMP-Timestamp` (Unix seconds) and `X-OMP-Signature: v1=<hex>` where the hex is HMAC-SHA256 of `timestamp + "." + raw body` keyed with the destination's secret (`ompwhs_` + 43 characters, sealed with `ENCRYPTION_KEY`, shown once, rotatable). Receivers should reject timestamps older than 5 minutes.
**Rotation** returns a new secret and, for `graceHours` (default 24, up to 72), signs with both (`v1=<new>, v1=<old>`). Body (`NotificationPayload`, at most 8 KiB, `version` 1): `id`, `type`, `occurredAt`, `instance {name, url}`, `team {id, slug, name}|null`, `url` (a portal link), `data`.
`slack`: `{"text": "<one line and a link>"}`, at most 3000 characters, `& < >` escaped, **unsigned** (the other `X-OMP-*` headers still go); no `rotate-secret` (`409 not_signed`).

| `data` of | Fields (never secrets, header values, call arguments or results, addresses) |
|---|---|
| `server.unhealthy` | `server {id, slug, name}`, `since`, `consecutiveFailures`, `errorClass`, `httpStatus?`, `reminder`, `failingHours?` (no health message, no URL) |
| `server.recovered` | `server`, `since`, `recoveredAt`, `downtimeSeconds` |
| `capability.changed` | `server`, `changedCount`, `newCount`, `held: true`, `capabilities[≤10] {kind, name≤100, change}`, `moreCount` |
| `publish.requested` | `server`, `reason`, `note≤300?`, `requestedBy {kind: user\|automation_token, name}` |
| `publish.decided` | `server`, `decision`, `note≤300?`, `decidedBy {name}` |
| `connection.needs_reconsent` | `provider {slug, name}`, `server?`, `reason` (`refresh_failed`, `upstream_rejected`); inbox and email only |
| `upstream_signin.lapsed` | `server`, `since`, `readAsName?` |
| `key.expiring` | `keyKind` (`personal`, `service_account`, `publisher_token`, `platform_token`), `name`, `expiresAt`, `daysLeft`, `serviceAccount?` (never a prefix) |

**Email.** Plain text only (no HTML, images or tracking), one message per recipient, `From: SMTP_FROM`, `Subject: [<INSTANCE_NAME>] <title>`, `Auto-Submitted: auto-generated`, `X-OMP-Event`, a link `APP_URL/…`, and a footer saying why they got it and where to change it.
Header values lose CR/LF and are RFC 2047-encoded. Dial 10 s, whole message 30 s. At most 30 emails per person per hour: beyond that the rows are `skipped` (the inbox still has them) and counted.
**Inbox rows** copy a server-composed `title` (≤ 200) and `body` (≤ 500) and a `LinkTarget`, so they outlive the event row.

## Data model (`00014_notifications.sql`, new tables only)

| Table | Contents |
|---|---|
| `notification_events` | `id bigint`, `public_id uuid` (UNIQUE; the payload `id`), `event` (CHECK the 8 names), `dedup_key` (UNIQUE ≤ 200), `team_id` (FK CASCADE), `subject_type`, `subject_id`, `recipient_user_id` (personal events), `actor_user_id`, `payload jsonb` (`data` only, CHECK ≤ 8 KiB), `occurred_at`, `fanned_out_at`. Partial index where `fanned_out_at IS NULL` |
| `notification_inbox` | `id bigint`, `user_id` (FK CASCADE), `event_id` (UNIQUE with `user_id`), `event`, `team_id`, `title`, `body`, `link jsonb`, `created_at`, `read_at`. Indexes (`user_id`, `id DESC`) and a partial one on unread |
| `notification_outbox` | `id`, `event_id` (FK CASCADE), `channel` (`email`\|`webhook`), `user_id` (email) or `destination_id` (webhook, FK CASCADE), `state` (`pending`, `sent`, `failed`, `skipped`), `attempts`, `next_attempt_at`, `last_attempt_at`, `sent_at`, `http_status`, `error_class`, `dedup_key` (UNIQUE), `created_at`. The recipient address is read from the profile at send time, never copied |
| `notification_preferences` | (`user_id`, `event`, `channel`) → `enabled`, `updated_at`; only rows that differ from the defaults |
| `notification_destinations` | `id`, `scope` (`team`\|`platform`), `team_id` (FK CASCADE, set iff team), `name` (UNIQUE per scope), `format`, `url_cipher` (AAD `notification_destination:<id>:url`), `url_display`, `secret_cipher`, `secret_previous_cipher`, `secret_previous_until`, `secret_hint`, `secret_rotated_at`, `events text[]`, `team_events`, `enabled`, `disabled_reason`, `consecutive_failures`, `last_success_at`, `last_failure_at`, `last_error_class`, `revision`, created/updated by and at. At most 10 per team and 10 platform |
| `notification_state` | One row: `last_run_at`, `key_scan_at` (Admin › Notifications shows the loop is alive) |

All `*_cipher` columns join `keyrotation.Columns` (a test already fails otherwise), so `rotate-keys` and `doctor` cover them. **Retention** is fixed, not a setting: inbox 90 days (read or not), events and outbox 14 days, deleted by step 4 in batches of `RETENTION_BATCH_SIZE`
(`omp_retention_deleted_total{kind="notification_inbox"|"notification_outbox"}`; the retention page's periods and `last_run` are unchanged).

## API (operations; roles and errors are in each description)

| Operation | Who |
|---|---|
| `listMyNotifications`, `getMyUnreadNotificationCount`, `markMyNotificationRead`, `markAllMyNotificationsRead`, `getMyNotificationPreferences`, `updateMyNotificationPreferences` | any signed-in person, their own only (`404 notification_not_found` for others') |
| `list/get/create/update/delete/test/rotate…Team NotificationDestination(s)`, `listTeamNotificationDeliveries` | team owners and admins write; platform staff read any team; editors and members `403`; `404` outside the team; `409 team_archived` on writes |
| `admin…NotificationDestination(s)` (list, create, get, update, delete, test, rotate, deliveries) | platform admins write, auditors read |
| `adminGetNotificationSettings` | platform admins and auditors: email on/off, From, host, port, mode, "authenticates", 24 h counts, queue depth, loop heartbeat; never username or password |
| `adminTestEmail` | platform admins: sends to **the caller's own address** only; `200` with `ok` and `errorClass`; `409 email_not_configured`; 5 per hour |

Errors: `400 invalid_name|invalid_format|invalid_event`, `409 name_taken|too_many_destinations|not_signed|team_archived`, `412/428` (If-Match on update), `422 url_not_allowed|invalid_events|not_receivable`, `429 rate_limited` (tests: 10 per destination per hour).
Tests report their outcome in a `200` body and are not stored in the delivery log. The delivery log is the last 50 queue rows of one destination (14 days): state, attempts, HTTP status, error class; never payloads or response bodies.
**Audit:** `notification_destination.create|update|delete|test|rotate_secret` (target `notification_destination`, label = name, team for team ones, before/after of name, events, enabled, `urlDisplay`; never URL or secret), `notification_destination.disable` (system, on auto-stop), `settings.notifications_test_email`. Preferences and read marks are personal and not audited.

## Security

Destinations' URLs are credentials (Slack): sealed, never returned (`urlDisplay` is scheme and host), so even `platform:read` tokens can't read them. Webhook payloads and emails carry names and counts only: no secrets, header values, health messages, URLs of servers, captured contents or e-mail addresses.
Personal events never leave to webhooks. Responses of destinations are discarded (only a status class and `errorClass` are kept), limiting the guarded dialer's use as a probe; tests and creation are rate limited. Mail goes only to portal users' profile addresses; the test mail only to the caller.
Everything is bounded: 8 KiB payloads, 10 destinations per scope, 200 events per tick, 30 emails per person per hour.

## UI

- **Bell** in the top bar: unread badge from `getMyUnreadNotificationCount` (60 s), a popover of the latest 8 with "Mark all read" and "View all"; **Inbox** (`/notifications`): All/Unread, rows route by `LinkTarget`, opening marks read.
- **Account › Notifications** (`/settings/notifications`): the events the person can receive × In-app / Email switches, a notice and disabled email column when `emailAvailable` is false, and where email goes.
- **Team settings › Notifications** (tab `notifications`, owners and admins; platform staff read-only): destinations with health badge, **Add** (format, URL, events), show-once secret panel, Test, Rotate secret (grace choice), delivery log (last 50), enable/disable, delete; a banner for a stopped destination.
- **Admin › Notifications** (`/admin/notifications`): Email card (status, **Send test email**), delivery queue card, platform destinations (same component, plus "also every team's events").

## Tests that must exist

Matrix classification: inbox and preferences own=signedIn; team destinations own=admins, foreign=platform (writes: none); admin destinations platform / padmin; settings platform; `adminTestEmail` padmin. Then:
config (every `SMTP_*` rule, `_FILE`, tls-none safety, doctor lines without secrets); per event: trigger, dedup, recipients by role (editors for server events, none for suspended, archived team, actor skipped), preferences; 3-failure threshold, reminder at 24 h, recovery only after an incident;
fan-out idempotence and a second dispatcher blocked by the lock; retry schedule, permanent failure, 24 h expiry, auto-stop at 20; the signature (a reference verifier, rotation with two `v1=`), payload ≤ 8 KiB and a sentinel that no secret or call content reaches payloads, emails, inbox, audit or logs;
SSRF: loopback, metadata, rebinding, redirect, plain http without allowlist, re-check at send; URL and secret never in any response; fake SMTP server (starttls required, AUTH, header injection attempt, 30/h cap); retention of inbox and outbox; Vitest for bell, inbox, preferences and both destination screens.
