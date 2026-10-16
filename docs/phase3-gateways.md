# Phase 3 — Gateways (design draft)

Status: **built** (backend and UI; see "As built" at the end). Open questions were answered as decisions 15–19 in
[decisions.md](decisions.md). Items still marked *proposed* (key rotation period, call outcomes) are defaults that may be
tuned.

## Goal

A gateway is a curated set of capabilities at `/mcp/{slug}`. A client connects with a credential, sees only what
it may use, and every upstream call carries a signed identity of the user. Calls are recorded as metadata.

## Data model

| Table | Columns (main) |
|---|---|
| `gateways` | id, slug (global, immutable), name, description, owner_kind `team`/`platform`, team_id (team gateways), enabled, capture_payloads (Phase 6; stored now, default false), revision, timestamps |
| `gateway_capabilities` | gateway_id, capability_id, min_team_role (team gateways only; nullable), added_by/at |
| `gateway_grants` | gateway_id, subject_kind `team`/`user`/`group`, team_id / user_id / group (exactly one), added_by/at |
| `signing_keys` | kid, alg `ES256`, private key (encrypted with `ENCRYPTION_KEY`), public JWK, state `next`/`active`/`retiring`/`retired`, created_at, activated_at, retire_after |
| `api_keys` | Grounded's personal keys: prefix, HMAC digest, user_id, name, expires_at, revoked_at, last_used_at (*proposed*: Phase 3, see Q1) |
| `mcp_calls` | time, gateway, server, capability, method, principal (user / key), client name, outcome, error class, latency, request/response bytes, request_id. No arguments or results |

`users.groups` (last-seen IdP groups from the configured groups claim) comes back from Grounded for group grants.

## Who may build what

- **Team gateways** — team owners/admins create and edit (decision 16). They may select:
  - any capability of the team's own servers (private or published, enabled, listed), and
  - approved capabilities of published catalog servers.
  Access: the team's members, filtered by each capability's optional minimum role.
- **Platform gateways** — platform admins create and edit. Only approved capabilities of published servers.
  Access: explicit grants to teams, users or SSO groups. No minimum-role setting.
- Auditors read everything; members can read the team's gateways and copy their connection details.

## Runtime (`/mcp/{slug}`)

- Streamable HTTP, stateless; `POST` only at first (`GET`/`DELETE` → 405). JSON responses; upstream SSE streams
  are relayed for `tools/call`.
- Methods: `initialize`, `ping`, `tools/list`, `tools/call`, `prompts/list`, `prompts/get`.
  *Proposed:* resources and resource templates come in a follow-up (URI rewriting needs care).
- Exposed names: `{server-slug}__{name}` for tools and prompts; titles and descriptions as approved.
- Every list and every call re-checks: credential valid, user active, gateway enabled, caller granted, capability
  still selected, role satisfied, server enabled, and either team-owned (team gateway) or published with the
  current fingerprint approved. A failed check on a call looks like "not found".
- Limits: 1 MiB request/response, the server's timeout, SSRF guard on every dial. Sampling, elicitation and roots
  are refused.
- Unauthenticated: `401` with `WWW-Authenticate: Bearer resource_metadata=".../.well-known/oauth-protected-resource/mcp/{slug}"`
  so Phase 4's OAuth slots in without changing clients.

## Identity sent upstream

- JWT, ES256, `kid` header. Claims: `iss` (APP_URL), `aud` (server URL), `sub` (user ID), `email`, `name`,
  `teams` (slugs), `gateway` (slug), `tool` (exposed name), `client` (key/client name), `iat`, `exp` = 5 min, `jti`.
- *Proposed* header: `Authorization: Bearer <jwt>` when the server has no static `Authorization` header (servers can
  act as ordinary OAuth resource servers and validate against JWKS); otherwise `MCP-Gateway-Identity: <jwt>`.
- `GET /.well-known/jwks.json` publishes the next, active and retiring keys. A new key is generated on first use and
  rotated every 30 days; the next key is published a day before it signs, and retiring keys stay published for one day
  after rotation.
- `docs/upstream-identity.md` will show servers how to verify the token (Go, Python, TypeScript snippets).

## Management API (to be added to `api/openapi.yaml`)

```text
GET/POST          /v1/teams/{team}/gateways
GET/PATCH/DELETE  /v1/teams/{team}/gateways/{gatewayId}
GET/PUT           /v1/teams/{team}/gateways/{gatewayId}/capabilities      (whole selection, If-Match)
GET               /v1/teams/{team}/gateway-options                       (selectable capabilities)
GET/POST          /v1/admin/gateways
GET/PATCH/DELETE  /v1/admin/gateways/{gatewayId}
GET/PUT           /v1/admin/gateways/{gatewayId}/capabilities
GET/POST/DELETE   /v1/admin/gateways/{gatewayId}/grants[/{grantId}]
GET               /v1/me/gateways                                        (what I can connect to, with URLs)
GET/POST/DELETE   /v1/me/api-keys[/{keyId}]                              (if Q1 = yes)
GET               /v1/teams/{team}/calls, /v1/admin/calls                (call metadata, filters)
GET               /.well-known/jwks.json                                 (outside OpenAPI, like /mcp)
```

## UI

- Team › Gateways: list, create (takeover), record with Overview (URL + "Connect" snippets for Claude Code,
  VS Code, Cursor), Capabilities (picker grouped by server, own vs catalog, min role), Activity (calls), Settings.
- Admin › Gateways: same plus Access (grants: teams, users, SSO groups) and the platform-wide call log.
- Workspace › My gateways: everything the person can use, with connect snippets.
- Account › API keys (if Q1 = yes).

## Tests that must exist

- Authz matrix for every new operation; a foreign team's capabilities can't be selected.
- Runtime: list shows only allowed items; revoke grant / unpublish server / change fingerprint / disable gateway /
  suspend user / revoke key between `tools/list` and `tools/call` → the call is refused without contacting the
  upstream.
- JWT: signature verifies against JWKS, `aud` is the server URL, expiry ≤ 5 min, rotation keeps old tokens valid
  until expiry; the client's credential is never forwarded upstream.
- No arguments/results in `mcp_calls` or logs.

## Resolved questions

1. Personal API keys ship in Phase 3 (own implementation). OAuth and service accounts in Phase 4.
2. Team gateways: owners and admins edit; members read and connect.
3. Team gateways are members-only; cross-team sharing uses platform gateways.
4. Tools and prompts first; resources later.
5. Identity header as proposed.

## As built (backend)

Code: `internal/gateways` (service, availability, grants), `internal/mcpgw` (`/mcp/{slug}`), `internal/identity`
(signing keys, JWKS, rotation loop), `internal/apikeys`, `internal/calls`, migration `00004_gateways.sql`. Server authors:
[upstream-identity.md](upstream-identity.md). Where this differs from or adds to the design above:

- **Selection.** New capabilities must be offered by gateway-options; capabilities **already selected may stay** in a
  `PUT` even while unavailable (for example a catalog server that left the catalog), since every call re-checks them.
  Exposed names must be unique **case-insensitively and across tools and prompts** (`422 name_clash`). Other errors:
  `400 duplicate_capability`, `400 invalid_role`, `422 min_role_not_allowed` (minRole on a platform gateway; the contract
  only says "must be absent"), `422 too_many`, `422 capability_not_selectable` (`details.capabilityIds`).
- **Availability order:** `gone` (also any non-tool/prompt), `team_archived` (the server's team, or a team gateway's
  own team, is archived), `server_disabled`, then for catalog capabilities `not_published`, `not_approved`, `changed`.
  A team gateway's own servers need no approval. `GatewayStats` (SQL) mirrors the Go rule; a test checks they agree.
- **Archived teams.** Archiving changes no data, so unarchiving restores everything. While a team is archived its team
  gateways answer `/mcp/{slug}` with `404` (lists and calls recorded as denied, `errorClass` `team_archived`), drop out
  of My gateways, and show every selected capability as `team_archived`; its servers leave the catalog,
  gateway-options and the health loop, and other teams' and platform gateways mark their capabilities `team_archived`
  (kept in a selection, not addable). Every list and call re-checks this.
- **Access.** Platform admins and auditors read every gateway but can only *use* a team gateway if they are members, and a
  platform gateway if granted. Group grants match `users.groups` exactly (case-sensitive); `/v1/me` returns the stored groups and
  `MyGateway.groups` names the caller's groups a gateway is granted to. Platform admins may switch a team gateway off/on (`PATCH enabled`), nothing else (`403 team_gateway`);
  `GET …/grants` of a team gateway is `[]`.
- **Runtime.** Lists are served from the stored, approved definitions (name replaced by the exposed name) without
  contacting servers, paged at 512 KiB with an opaque cursor. Calls are buffered (the SDK client reads an upstream SSE
  stream; no relay), response ≤ 1 MiB. Negotiated versions: 2025-11-25, 2025-06-18, 2025-03-26, 2024-11-05. An
  `MCP-Protocol-Version` header outside these (and older than 2026-07-28) is `400`. `server/discover` is "method not
  found", so 2026-07-28 clients fall back to `initialize`. JSON-RPC batches are `400`. A foreign `Origin` is `403`. Unknown
  or disabled gateways and no access are HTTP `404`; refused calls are JSON-RPC `-32602 Unknown tool/prompt`.
  Notifications get `202`. `clientInfo.name` is recorded when the same request carries it (`initialize`, or the
  2026-07-28 `_meta` `io.modelcontextprotocol/clientInfo`); otherwise omitted.
- **Call records.** Denied rows carry `errorClass`: `gateway_disabled`, `no_access`, `role`, `unknown`, or an
  unavailability reason. Upstream failures use the registry's error classes; JSON-RPC invalid params from the server is
  `bad_request`. Initialize and ping are not recorded. `mcp_calls` is append-only (trigger, like `audit_log`).
- **Identity JWT.** Adds `nbf` (iat − 30 s). `prompts/get` carries `prompt` instead of `tool`. `client` falls back to the
  API key's name. A replica caches the active key for 1 minute; the rotation loop (advisory lock
  `omg.signing-key-rotation`) checks hourly. The first key on an empty table is created on first use or at start-up
  and signs at once. After that the loop creates the next key (state `next`: published in the JWKS, not signing) at
  least `SIGNING_KEY_PREPUBLISH` (default 24h, 0–7 days) before the active key has signed for 30 days, and promotes it
  once the active key is due and the next key has been published that long (a late next key delays the rotation).
  Verifiers should still refetch the JWKS for an unknown `kid`. Retiring keys stay published for 1 day, then their
  private key is erased (migration `00005_signing_key_next`).
- **API keys.** `omcp_` + 43 URL-safe characters; HMAC-SHA256 with `API_KEY_PEPPER` (32 bytes base64, required by
  `serve`, the `.env.example` value refused off loopback). Revoking a revoked key is a no-op `200`.
- **Audit.** `gateway.create/update/delete/capabilities_set` (target `gateway`, team set for team gateways),
  `gateway.grant_add/grant_remove` (target `gateway_grant`), `api_key.create/revoke` (target `api_key`), all with live
  labels.
- **Not built yet:** resources, the protected-resource metadata endpoint (Phase 4), payload capture (`capture_payloads`
  stays false), rate limits on `/mcp`.

Results relayed from upstream servers have `_meta["io.modelcontextprotocol/serverInfo"]` removed: clients talk to the
gateway, and the upstream's own name and version are not theirs to see. Other `_meta` entries pass through unchanged.
