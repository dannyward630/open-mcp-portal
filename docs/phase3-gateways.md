# Phase 3 — Gateways (design draft)

Status: **approved, in progress**. Open questions were answered as decisions 15–19 in [decisions.md](decisions.md).
Items still marked *proposed* (key rotation period, call outcomes) are defaults that may be tuned.

## Goal

A gateway is a curated set of capabilities at `/mcp/{slug}`. A client connects with a credential, sees only what
it may use, and every upstream call carries a signed identity of the user. Calls are recorded as metadata.

## Data model

| Table | Columns (main) |
|---|---|
| `gateways` | id, slug (global, immutable), name, description, owner_kind `team`/`platform`, team_id (team gateways), enabled, capture_payloads (Phase 6; stored now, default false), revision, timestamps |
| `gateway_capabilities` | gateway_id, capability_id, min_team_role (team gateways only; nullable), added_by/at |
| `gateway_grants` | gateway_id, subject_kind `team`/`user`/`group`, team_id / user_id / group (exactly one), added_by/at |
| `signing_keys` | kid, alg `ES256`, private key (encrypted with `ENCRYPTION_KEY`), public JWK, state `active`/`retiring`/`retired`, created_at, retire_after |
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
- `GET /.well-known/jwks.json` publishes active and retiring keys. A new key is generated on first use and rotated
  every 30 days (*proposed*); retiring keys stay published for one day after rotation.
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
