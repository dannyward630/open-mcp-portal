# Phase 4 — Client sign-in (OAuth) and service accounts (design draft)

Status: **built** (backend, UI and the Phase 4 QA follow-ups; see "As built" at the end). Builds on decisions 12, 15–17; open questions answered as decisions 21–24.
Grounded's OAuth server (`internal/oauth`, `docs/mcp.md` "Signing in with OAuth") is the reference, not a dependency.

## Goal

1. Interactive MCP clients (Claude Code, Claude Desktop/claude.ai, VS Code, Cursor) connect to `/mcp/{slug}` with
   no key: the person signs in with the company IdP in a browser, approves the client once, and the client gets
   short-lived tokens it refreshes itself.
2. Headless agents, CI and scripts use **service accounts**: team-owned identities whose keys don't depend on a
   person staying on the team.

## OAuth: the gateway as authorization server

Follows the MCP authorization spec (OAuth 2.1, RFC 9728 protected-resource metadata, RFC 8414, RFC 8707
resource indicators, PKCE S256, RFC 9207 `iss`), like Grounded.

| Endpoint | Purpose |
|---|---|
| `GET /.well-known/oauth-protected-resource/mcp/{slug}` | Resource metadata: `resource` = `<APP_URL>/mcp/{slug}`, authorization server = `<APP_URL>` (already advertised in today's `401`) |
| `GET /.well-known/oauth-authorization-server` | AS metadata: code + PKCE S256 only, public clients (`none`), CIMD supported, DCR endpoint |
| `GET /oauth/authorize` | Authorization code + PKCE; requires `resource=<APP_URL>/mcp/{slug}` exactly; signs the person in (existing OIDC session or redirect) then shows consent in the SPA |
| `POST /oauth/token` | `authorization_code` and `refresh_token` grants |
| `POST /oauth/register` | Dynamic Client Registration (public clients only, rate limited, unused registrations expire after 30 days) |
| `POST /oauth/revoke` | RFC 7009 |

- **Clients:** Client ID Metadata Documents preferred (fetched with the SSRF guard, ≤16 KiB, no redirects, cached);
  DCR for older clients, marked **Unverified** on the consent page. Redirect URIs: https, or http on loopback
  (any port).
- **Tokens** (*proposed*, Grounded's values): code 60 s single use; access token `omat_…` 1 h; refresh token
  `omrt_…` 30 days, rotated on every use, reuse after 30 s revokes the connection. Opaque, stored as HMAC digests
  with `API_KEY_PEPPER`.
- **One connection per gateway** (*proposed*): a token is bound to one gateway (`resource`), matching how
  clients add one URL per server. Access is still re-checked on every request.
- **Consent** per person, client and gateway; shows the client (verified host or "Unverified"), the gateway, and
  what it can do. **Trusted clients** (decision 12): platform admins keep an allowlist of client IDs that skip the
  consent screen, seeded with Claude (`https://claude.ai/oauth/mcp-oauth-client-metadata`), Claude Code
  (`https://claude.ai/oauth/claude-code-client-metadata`) and VS Code (`https://vscode.dev/oauth/client-metadata.json`).
  DCR clients can't be trusted (their IDs change).
- **Connected apps:** Account › Connected apps lists clients per gateway with last use and Disconnect; platform admins
  see and disconnect anyone's (user detail page). Suspending a user or removing their access stops tokens at the
  next request.
- **Audit:** `oauth.client_register`, `oauth.consent`, `oauth.token_issue`, `oauth.revoke` (reason), never a token.
- **Calls:** principal kind `oauth` with the client name.

## Service accounts

- Team-owned; created, edited, disabled and deleted by team **owners and admins**. Fields: name, description,
  **team role** used for minimum-role checks (*proposed*: `member` or `editor`; never owner/admin), enabled.
- **Keys:** `omcp_sa_…`, HMAC digest, always expire (max 365 days), at most 2 active per account so keys can be
  rotated without downtime; optionally limited to gateways like personal keys. Shown once.
- **Access** (*proposed*): the team's own gateways (with the account's role), and platform gateways granted to the
  team (a team grant covers the team's service accounts). No direct grants to individual service accounts.
- **Identity JWT:** `sub` = service account ID, new claim `principal_type` = `service_account` (and `user` for
  people), `name` = account name, `teams` = [owning team], no `email`. Upstream docs updated.
- A service account outlives its creator; the team's owners/admins are its responsible people. Archiving the team
  stops it.
- **Calls:** principal kind `service_account`.

## UI

- Team › Service accounts: list, create, record (keys with create-once secret, rotate, revoke; role; enabled).
- Account › Connected apps; Admin › Users › person › Connected apps.
- Admin › Trusted clients (allowlist editor).
- Consent page (`/oauth/consent`) in the SPA, Grounded-style.
- Connect snippets: OAuth becomes the default for Claude Code / VS Code / Cursor (no header); API-key variants stay
  available for headless use.

## Tests that must exist

- Full OAuth flow with a fake client against httptest: metadata, PKCE (plain refused), resource mismatch →
  `invalid_target`, code reuse, refresh rotation and reuse detection, revoke, suspended user, removed access.
- CIMD fetch rules (private address, redirect, size, content type); DCR rate limit and expiry.
- Trusted client skips consent; untrusted/DCR never does.
- Service accounts: authz matrix, key rotation, role/minRole, team grant coverage, archived team, JWT claims.
- Secrets never in responses (beyond creation), audit or logs.

## Resolved questions

All four recommendations accepted (decisions 21–24).

## As built (backend)

Code: `internal/oauth` (authorization server: `authorize.go`, `clients.go`, `metadata.go`, `token.go`, `grants.go`,
`trusted.go`), `internal/serviceaccounts`, `internal/ratelimit`, `internal/httpapi/oauth.go` (protocol endpoints),
`oauth_consent.go` and `service_accounts.go` (`/v1`), and changes to `internal/mcpgw` (three principal kinds),
`internal/gateways` (`ServiceAccountCaller`, `UsableByServiceAccount`), `internal/identity` (`principal_type`),
`internal/calls`, `internal/auth` (`SessionUser`, `LoginURL`). Migration `00008_client_auth.sql`. People's guide:
[connect-clients.md](connect-clients.md); server authors: [upstream-identity.md](upstream-identity.md).

**OAuth.** Always on (no setting). Grounded's rules, adapted:

- **Endpoints** (outside the OpenAPI contract; the contract test skips `/oauth/` and `/.well-known/`):
  `GET /.well-known/oauth-protected-resource/mcp/{slug}` (`resource`, `authorization_servers` = `[APP_URL]`,
  `bearer_methods_supported`, `resource_name` = instance name; served for **any** well-formed slug without looking the
  gateway up, so it doesn't reveal which gateways exist; a malformed slug is `404`), `GET /.well-known/oauth-authorization-server`
  (code + `S256` only, `none` auth, `client_id_metadata_document_supported`, `authorization_response_iss_parameter_supported`,
  registration and revocation endpoints; no `scopes_supported`: there are no scopes, a `scope` parameter is ignored and
  the token response has none), `GET /oauth/authorize`, `POST /oauth/token`, `POST /oauth/register`, `POST /oauth/revoke`
  (`GET` on the three POST endpoints is `405`, never the web app). The `/mcp` 401 challenge is unchanged.
- **Authorize order.** Query > 8000 characters, a repeated parameter, an unknown/unfetchable client, a missing or
  unregistered `redirect_uri` or a `state` over 1000 characters: an HTML **page** (400, never a redirect). Then errors go
  back to the client with `state` and `iss`: `unsupported_response_type`, `invalid_request` (no PKCE, `plain`, malformed
  challenge), `invalid_target` (resource missing or not exactly `<APP_URL>/mcp/{slug}`). Then the **browser session**:
  none → `302` to sign in and back (`/auth/login?next=…` with OIDC; the web app's sign-in page `/?next=…` with only the
  development login); a suspended person gets a page. Signed in, the resource must be a gateway they may use **now**
  (exists, enabled, team not archived, access); otherwise `invalid_target` (unknown, off and no-access look the same,
  checked only after sign-in). Then a **remembered** consent (active grant for person + client + gateway) or a
  **trusted** client issues the code at once; anyone else is sent to `/oauth/consent?<the same query>`.
- **Consent API.** `GET /v1/oauth/consent` re-checks the request: problems are `400` with code `oauth_<error>` (for
  example `oauth_invalid_request`, `oauth_invalid_client`), a resource that isn't a gateway the caller can use is
  `404 gateway_not_found`. `client.trusted` is true only for metadata clients on the list. `POST` (CSRF token) with
  `allow` creates the grant (audited `oauth.consent`, metadata `how: allowed`) and a code; `deny` returns
  `access_denied`. The query is re-validated on POST, so the page can't widen anything.
- **Clients.** Metadata documents are fetched with `internal/upstream`'s address guard (`Client.GuardedDial`): public
  addresses; internal ones only inside the allowlist of internal networks (decision 20); loopback, link-local and
  metadata never — except with the development allowance, which also accepts a document at a plain-`http` loopback URL
  (production: https only). No proxy, no redirects, 5 s, ≤ 16 KiB, `application/json` (or `+json`), `client_id` must
  equal the URL, cached for `max-age` clamped to 1 min–1 h (10 min default). DCR: public clients only, IDs `omcl_…`,
  10/min/address, unused registrations deleted after 30 days (on the next registration; an expired one is refused at
  once); audited `oauth.client_register` by the system with the client IP.
- **Trusted clients.** `oauth_settings` (one row, own revision), seeded with Claude, Claude Code and VS Code. Entries
  must be https metadata-document URLs with a path (`422 invalid_client_id`, `details.index`, `details.reason`
  `not_metadata_url`/`duplicate`/`too_many`; a label over 100 characters is `400 invalid_label`). Read from the database
  on every authorization (no cache). A trusted client's first connection is still audited `oauth.consent` (`how: trusted`).
- **Tokens.** Code `omac_` 60 s single use (deleted before any check); access `omat_` 1 h; refresh `omrt_` 30 days,
  rotated; reuse within 30 s gets another pair, later reuse revokes the grant (`refresh_reuse`). Stored as hex
  HMAC-SHA256 of `"oauth:" + token` under `API_KEY_PEPPER` (no previous-pepper support: rotating the pepper signs every
  client out). An access token is accepted only by `/mcp/{slug}` of its gateway (another gateway: `401 invalid_token`
  with that gateway's challenge) and never by `/v1`. Every request re-checks token expiry, grant, person active (`401`),
  and access to the gateway (`404`, recorded `denied`), like API keys. Grants are deleted with their gateway.
- **Revocation.** RFC 7009: a refresh token revokes the connection, an access token only itself; another client's
  token or junk is a no-op `200`. Connected apps: `DELETE /v1/me/oauth-grants/{id}` (reason `user`), platform admins
  `DELETE /v1/admin/users/{id}/oauth-grants/{id}` (reason `admin`; auditors `403`). Unknown user on the admin list: `404`.
- **Rate limits** per address and process (in memory, like sign-in): 60/min on authorize, token and revoke, 10/min on
  register; `429 slow_down` with `Retry-After`.
- **Audit:** `oauth.client_register` (target `oauth_client`), `oauth.consent`, `oauth.token_issue` (`grantType`),
  `oauth.revoke` (`reason`; `token: access` for a single access token) on target `oauth_grant` (label
  "client · gateway"; team set for team gateways on consent and revoke), `settings.oauth_clients_update` (target
  `platform_settings`/`oauth_clients`). Actions by a client carry `via: oauth` and `client`. Never a token.

**Service accounts.** Names `^[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?$` unique per team (`409 name_taken`), at most 100 per
team (`409 too_many_service_accounts`), role `member`|`editor` (`400 invalid_role`). Owners/admins write (platform roles
`403` through team routes, like gateways); members and platform staff list/get; owners/admins and platform staff list
keys. Archived teams are read-only (`409 team_archived`). Keys `omcp_sa_` + 43 characters (prefix stored `omcp_sa_` + 8),
HMAC with the pepper, 1–365 days (default 90), at most 2 active (`409 too_many_keys`), not for a turned-off account
(`409 disabled`), optional `gatewayIds` (≤ 50 after de-duplication, each one the **account** can use now, else
`422 gateway_not_usable`). Revoking a revoked key is a no-op. Deleting an account deletes its keys; call records keep
its name (`mcp_calls.service_account_name`, `key_name`).

- **Runtime.** A turned-off or deleted account, an archived team, or a revoked/expired key is `401` at the next request.
  Access: the team's own gateways (role used for `minRole`) and platform gateways with a **team grant** to its team
  (user and group grants never cover it). Key scopes behave like personal ones (`key_scope`).
- **Identity JWT:** every token now has `principal_type` (`user` | `service_account`); a service account's has `sub` =
  its ID, `name`, `teams` = [its team], no `email`; OAuth calls' `client` is the client's name, with `client_verified`
  (QA follow-ups below).
- **Call records:** `principal_kind` `api_key` | `oauth` | `service_account`; `user_id` is NULL for service accounts
  (a CHECK ties the columns to the kind). `CallPrincipal`: `userId`/`email` for people, `apiKeyName` for keys,
  `oauthClientName` for OAuth, `serviceAccountId`/`serviceAccountTeamSlug` for accounts. `?serviceAccount=` filters
  team and admin call lists. Team call lists mean "through the team's gateways, to its servers, or by its service
  accounts" (the last added by the QA follow-ups below).
- **Audit:** `service_account.create/update/delete` (target `service_account`) and `service_account_key.create/revoke`
  (target `service_account_key`, label "account · key"), all with the team; never a secret.

**Contract:** no changes to `api/openapi.yaml` were needed for the backend; the QA follow-ups below added some.

**Tests:** `internal/oauth` (address guard with netpolicy entries for documents, cache, PKCE, redirect matching, resource
shape, trusted-list validation), `internal/httpapi/oauth_integration_test.go` (discovery, authorize validation as page vs
redirect, sign-in redirect, consent allow/deny/CSRF, remembered and trusted consent, registered clients never trusted,
code reuse, wrong verifier, refresh rotation and reuse revocation, revoke endpoint, token bound to its gateway, connected
apps, DCR expiry, document fetch rules, rate limits, re-checks without reaching the server, token sentinel),
`service_accounts_integration_test.go` (management, key limits and rotation, role vs `minRole`, team-grant coverage,
off/archived/expired/revoked/deleted refusals, JWT claims verified by the fake server, scope, call filter, secret
sentinel), and the authz matrix for every new operation.

**Not built / gaps:** rate limits on `/mcp` itself; per-tool scopes or step-up; private-use redirect schemes; CORS on
the OAuth endpoints; previous-pepper support; DCR expiry runs only when someone registers (an expired registration is
refused regardless); rate counters are per process.

## As built: QA follow-ups

**Contract changes** (additive; existing clients keep working):

- `GET /v1/teams/{team}/service-accounts/{serviceAccountId}/gateways` (`listServiceAccountGateways`; members and above,
  platform staff read any team): the gateways the account can use now, as `MyGateway` items ordered by name: its team's
  enabled gateways (`access: [team_member]`) and enabled platform gateways granted to its team (`access: [team_grant]`).
  `capabilityCount` is what the account reaches (availability, and its role for `minRole` on team gateways). Empty
  while the team is archived; a turned-off account still lists them. `gateways.Service.ListForServiceAccount`,
  `serviceaccounts.Service.Gateways`, query `ListGatewaysForServiceAccount`.
- `GET /v1/admin/service-accounts` (`adminListServiceAccounts`; platform admins and auditors): a `ServiceAccountPage`
  (new schema) ordered by (name, id) with `q` (account name, team slug or name), `team` (slug or ID; unknown lists
  nothing), `cursor`, `limit`. Query `AdminListServiceAccounts`.
- `GET /v1/teams/{team}/calls` (and Admin › Calls' `team` filter) also lists calls made by the team's service accounts
  through **any** gateway (a platform gateway granted to the team, or a refusal on another team's gateway); documented in
  the operation's description. Index `mcp_calls_service_account_team_idx`.
- `CallPrincipal.serviceAccountExists` (service_account: false once the account is deleted) and
  `CallPrincipal.oauthClientVerified` (oauth: true for metadata-document clients, false for registered ones). Calls now
  record the client's kind (`mcp_calls.oauth_client_kind`); older records read it from the grant while it exists.
- Identity JWT: `client_verified` (bool) whenever `client` is an OAuth client's name; absent otherwise.
  [upstream-identity.md](upstream-identity.md) says a registered client's `client` is self-asserted.

Migration `00009_call_principals.sql` (a nullable column and an index).

**Behaviour and UI:** Client ID Metadata Document fetches are logged at info level ("oauth client metadata document
fetched": host, HTTP status, outcome such as `ok`, `http_status`, `blocked_address`, `redirect`, `too_large`, and the
duration; never the document or its path). The "Can't connect this app" page follows the person's color mode (the
app's `/color-mode.js`, CSP `script-src 'self'`, dark styles; the system's setting without script). Service account
Overview's Access card and the key dialog's "Limit to gateways" use the new endpoint (counts per the account's role;
granted platform gateways listed and choosable). Admin › Calls' service account filter lists every team's accounts
(searchable, "name (team)"; the chosen team's when a team is chosen). Call rows: a deleted service account is plain
text ("Deleted service account · team · Key: …"), service account rows name the key, apps that registered themselves
read "via <name> (unverified)". The key dialog shows "Next: finish the rotation" only when another key was active
before the new one (captured when Create is pressed), once. The sign-in page reached from `/oauth/…` says an app is
waiting to connect. Suspend-user and team-grant dialogs say what they cover. The service account header says "Never
used"/"Used …" and shows one notice for turned off and/or archived. The trusted-client dialog focuses the first
invalid field.

**Tests:** `TestServiceAccountGatewaysAndAdminList` (role vs `minRole`, team grant vs person grant, other team's and
turned-off gateways, archived team, 404s, admin list search/team/paging/403/400), `TestServiceAccountsAtTheGateway`
(team call list includes the account's calls through platform and other teams' gateways; `serviceAccountExists`
before and after delete; no `client_verified` for keys), OAuth flow tests (`client_verified` true/false in the JWT,
`oauthClientVerified`, recorded kind; the error page's color mode and CSP), `TestMetadataDocumentFetchLogging`, the
authz matrix for both new operations, the doctor's migration version. Vitest: service accounts (Access card, key
scope with a platform gateway, rotation note only after a previous key, one combined notice, "Never used"), calls
(unverified apps, deleted accounts, key names, admin filter without a team), sign-in note, trusted-client focus,
suspend and grant wording.
