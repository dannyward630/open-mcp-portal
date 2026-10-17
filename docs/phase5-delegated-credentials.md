# Phase 5: Delegated credentials (design)

Status: **designed, not built**. Decisions 25–30 in [decisions.md](decisions.md) are defaults the assistant
accepted under the instruction "continue until both are done". The contract is in [`../api/openapi.yaml`](../api/openapi.yaml)
(tags `auth-providers` and `connections`). Builds on decisions 7–8, 12, 19 and 21–24. Inspired by Arcade's tool-level
authorization (`requires_auth=GitHub(scopes=[…])`) and MCP 2025-11-25 URL-mode elicitation (`URLElicitationRequiredError`, `-32042`).

## Goal

A tool can act **as the person** on a third-party service. Examples: create a GitHub issue as them, or read their
Google Drive. A platform admin configures the provider's OAuth app once. A server's team says which provider and scopes
its capabilities need. The first call asks the person to connect their account in a browser. After that, the gateway
sends their token with each call that needs it and refreshes it. The client and the model never see the token.

## Data model (migration `00010_delegated_credentials`)

| Table | Columns (main) |
|---|---|
| `auth_providers` | id, slug (unique, immutable), name, kind `oauth2`, preset, enabled, client_id, client_secret (sealed with `ENCRYPTION_KEY`, AAD = provider ID), client_secret_hint, token_auth_method, pkce, authorization_url, token_url, revocation_url, userinfo_url, default_scopes `text[]`, scope_param, scope_separator, token_response_format, extra_authorize_params `jsonb`, logo_url, revision, created_by, timestamps |
| `mcp_servers` (+) | auth_provider_id (nullable, FK `RESTRICT`), auth_scopes `text[]` |
| `mcp_capabilities` (+) | requirement_mode `inherit`/`none`/`scopes` (default `inherit`), requirement_scopes `text[]` |
| `provider_connections` | id, user_id, provider_id (unique together), status `active`/`needs_reconsent`, scopes `text[]` (granted), account_label, access_token and refresh_token (sealed, AAD = connection ID + field), expires_at, connected_at, refreshed_at, last_used_at, last_error_class |
| `provider_connect_requests` | id_digest (SHA-256 of a random 32-byte ID, base64url), user_id, provider_id, required_scopes, gateway_id, capability_id, exposed_name, reason, created_at, expires_at (+10 min), used_at |
| `provider_auth_states` | state_digest, user_id, session_digest, provider_id, scopes asked, code_verifier (sealed), request_digest, return_to, created_at, expires_at (+10 min). Single use |
| `mcp_calls` (+) | provider_slug; outcome `needs_authorization` |

`ENCRYPTION_KEY_PREVIOUS` still decrypts, as for server headers. Expired requests and states are deleted when new ones
are written and by the retention loop.

## Roles

| Action | Who |
|---|---|
| Configure providers (create, edit, turn off, delete) | platform admins |
| Read the provider configuration and presets | platform admins and auditors |
| List providers for picking (`GET /v1/auth-providers`: id, slug, name, kind, preset, default scopes, logo) | any active signed-in person |
| Set a server's requirement and capability overrides | team editors, admins and owners (the registry's write roles); platform roles never through team routes |
| Review requirements (part of server review) | platform admins |
| See, connect and disconnect your own accounts | every person (browser session for connect) |
| See someone's connections (metadata) | platform admins and auditors; revoke: platform admins only |
| Use capabilities that require a provider | people only; service accounts are refused (decision 29) |

## Auth providers (decision 25)

Generic OAuth 2.0 authorization code. PKCE S256 is on by default; turn it off only where the provider rejects it. The
token endpoint uses `client_secret_post` (the default), `client_secret_basic` or `none`; `none` makes a public client and
requires PKCE. `tokenUrl`, `revocationUrl` (RFC 7009) and `userinfoUrl` are dialled through `internal/upstream`'s address
guard and the internal-network allowlist (decision 20): no proxy, no redirects, 10 s, responses ≤ 64 KiB.
`authorizationUrl` is only visited by browsers, but it must still be https (plain http only on loopback in
development). Every URL is checked like a server URL: no user info, query or fragment.

There are two settings for Slack's non-standard user-token flow. `scopeParam` is `user_scope` instead of `scope`, and
`tokenResponseFormat` `slack_authed_user` reads the token from `authed_user`. Reserved `extraAuthorizeParams` keys are
refused. The redirect URI is always `<APP_URL>/connect/callback`, shown on the provider record for the admin to register.

Presets only pre-fill the form. Check the values against each provider's documentation when building:

| Preset | Endpoints (authorize · token · revoke · userinfo) | Notes |
|---|---|---|
| GitHub | `github.com/login/oauth/authorize` · `/access_token` · none · `api.github.com/user` | PKCE supported (2025). OAuth-app tokens don't expire. Send `Accept: application/json` |
| Google | `accounts.google.com/o/oauth2/v2/auth` · `oauth2.googleapis.com/token` · `/revoke` · `openidconnect.googleapis.com/v1/userinfo` | `access_type=offline`, `prompt=consent`, `include_granted_scopes=true`. Use full scope URLs |
| Microsoft Entra ID | `login.microsoftonline.com/{tenant}/oauth2/v2.0/authorize` · `/token` · none · `graph.microsoft.com/oidc/userinfo` | `tenant` defaults to `organizations`; add `offline_access` for refresh |
| Atlassian | `auth.atlassian.com/authorize` · `/oauth/token` · none · `api.atlassian.com/me` | `audience=api.atlassian.com`, `prompt=consent`, `offline_access`. PKCE off |
| Slack | `slack.com/oauth/v2/authorize` · `slack.com/api/oauth.v2.access` · none · none | `user_scope`, comma separator, `slack_authed_user`. PKCE off |
| GitLab | `{host}/oauth/authorize` · `/oauth/token` · `/oauth/revoke` · `/oauth/userinfo` | `host` defaults to `gitlab.com` (self-managed) |

Changing `clientId` marks every connection `needs_reconsent` and erases its tokens; `auth_provider.update` records
`connectionsReset`. Turning a provider off refuses its calls and new connections; connections are kept. Delete is
`409 in_use` while any server requires the provider. Otherwise delete erases every connection and revokes it at the
provider where possible.

## Requirements (decision 26)

- **Server:** optional `requirement` `{providerId, scopes}`. Empty scopes mean the provider's default scopes. Only an
  enabled provider can be newly chosen.
- **Capability override** (`PUT …/capabilities/{id}/requirement`, If-Match = the server's revision): `inherit`, `none`, or
  `scopes` (the same provider with other scopes; `409 no_server_requirement` without one). Removing the server's
  requirement, or changing its provider, resets `scopes` overrides to `inherit`.
- The token goes to the **server**. Overrides decide where consent is asked and which scopes are sent, not which of
  the server's code can see a token. One provider per call.
- **Review:** under `approval_required`, a published server goes back to `pending` (`mcp_server.publish_request`, reason
  `requirement_changed`) when any capability's effective requirement **widens**. That means a provider where there
  was none, another provider, or a scope that wasn't in effect before. Removing or narrowing a requirement never needs
  review, because it sends less. Under `self_publish` the server stays published. Reviewers see the requirement and
  the overrides.
- **Views:** `McpServer.requirement`, `McpCapability.requirementOverride` and `requires` (the effective requirement),
  and `requires` on catalog capabilities, gateway capabilities and gateway options. In `MyGatewayDetail`, `requires.connected`
  says whether a call would be forwarded now. Admin › Servers filters with `?provider=`.

## Runtime (decisions 27–29)

The existing checks (credential, person active, gateway, grant, selection, role, availability) run first, so anything
not allowed still looks like "not found". Then, for `tools/call`, `prompts/get` and `resources/read` of a capability
with an effective requirement:

1. **Service account:** JSON-RPC `-32010` "This tool needs a person's GitHub account; service accounts can't use it".
   `data.reason` is `service_account`. Recorded as `denied`, class `service_account`. Lists still show the tool.
2. **Provider turned off:** `-32010`, `data.reason` `provider_disabled`. Recorded as `denied`, class `provider_disabled`.
3. **No active connection, or granted scopes don't cover the required ones:** the gateway creates (or reuses for the
   same person, capability and scopes) a connect request and answers `-32042`. Nothing is forwarded. Recorded as
   `needs_authorization` with class `not_connected`, `missing_scopes` or `needs_reconsent`. At most 20 open requests
   per person; the oldest is dropped.
4. **Access token expires within 60 s:** refresh it while holding a row lock (`SELECT … FOR UPDATE`), so replicas
   never use a rotating refresh token twice. `invalid_grant` or a 400/401: erase the tokens, mark the connection
   `needs_reconsent`, audit `provider_connection.refresh_failed`, then go to step 3. A network error, 5xx or timeout:
   `-32010` `provider_unavailable` (recorded as `upstream_error`), and the connection is kept. An expired token with
   no refresh token counts as a failed refresh with reason `expired`.
5. **Forward** with `MCP-Gateway-Provider: <slug>` and `MCP-Gateway-Provider-Token: <access token>` (bare, no
   `Bearer`). The identity JWT (decision 19, unchanged) gains `provider: "<slug>"`. Calls without a token never get
   these headers or the claim. `last_used_at` is updated at most once a minute.

```json
{"jsonrpc": "2.0", "id": 7, "error": {"code": -32042,
 "message": "github-tools__create_issue needs your GitHub account (repo). Open https://mcp.example.com/connect/github?request=Zk3… to connect it, then try again.",
 "data": {"elicitations": [{"mode": "url", "elicitationId": "Zk3…",
   "url": "https://mcp.example.com/connect/github?request=Zk3…",
   "message": "Connect your GitHub account (repo) to use github-tools__create_issue in the support gateway."}]}}}
```

The gateway is stateless (JSON responses, no session stream), so it never sends `notifications/elicitation/complete`.
The person retries the call; the spec allows manual retry. Client capabilities are only sent on `initialize`, so the
gateway can't tell whether a later request comes from a client that supports URL elicitation. Every client therefore
gets `-32042`, and its `message` carries the link for clients that only show errors. The URL contains only an opaque
ID: no personal data, and it doesn't sign anyone in. Static header names `MCP-Gateway-Provider` and
`MCP-Gateway-Provider-Token` become reserved (`400 invalid_auth_header`), like `MCP-Gateway-Identity`.

```text
Client          Gateway /mcp/{slug}            Browser (person)          Provider              MCP server
  │ tools/call ──►│ checks pass; no connection
  │◄── -32042 ────│ (url /connect/github?request=R)
  │ shows link ──────────────────────────────►│ /connect/github?request=R (web app, signed in as same person)
  │               │◄── GET /v1/connect/requests/R, POST /v1/me/connections/start {providerId, requestId}
  │               │──── authorizeUrl (state, PKCE) ─►│── authorize ─────►│ consent
  │               │◄──────────── /connect/callback?code&state ───────────│
  │               │── token exchange (guarded) ──────────────────────────►│
  │               │ seal tokens, mark R used ──► 303 /connect/done "Go back and try again"
  │ tools/call ──►│ token valid (refresh if needed) ── JWT + MCP-Gateway-Provider[-Token] ─────────────►│
  │◄── result ────│◄──────────────────────────────────────────────────────────────────────────────────│
```

## Connect and callback

- **`/connect/{provider}?request=R`** is a web-app route; the server serves the SPA, as it does for `/oauth/consent`. A
  signed-out person goes to sign-in and comes back. The page loads `GET /v1/connect/requests/R`. For another person's
  or an unknown request it shows "This link was made for someone else" (404). For an expired or used one it shows
  "This link expired. Run the tool again." (410). Otherwise it shows a confirm page: who is signed in, the tool and
  gateway, the provider, the account connected now, and **each scope** (already granted, or new). Continue calls
  `POST /v1/me/connections/start {providerId, requestId}` (CSRF token, same origin, browser session only), then sends
  the browser to `authorizeUrl`.
- **Start** asks for the union of the granted scopes (if active), the request's or caller's scopes, and, with neither,
  the provider defaults. A new token therefore replaces the old one without losing scopes. It stores a single-use
  state (256-bit, digest stored) bound to the **session digest, person, provider, request and return path**. With
  PKCE, the verifier is sealed. 20 per minute per person.
- **`GET /connect/callback?code&state`** is a server route outside the contract; the contract test skips `/connect/`.
  1. Delete the state before any other check; missing or expired shows an error page.
  2. The browser session must be the one that started the flow (else "Sign in as the person who started
     connecting"). This stops OAuth CSRF (an attacker's account attached to someone else's session); the state names
     the provider, which defeats mix-up attacks. A provider `error` goes to `/connect/done?provider=…&result=denied`.
  3. Exchange the code (with the verifier) at the token URL through the guard; JSON and form-encoded responses are
     accepted. Granted scopes are the response's `scope` when present, else those asked for. `userinfo` gives
     `accountLabel` (5 s; a failure is ignored).
  4. Upsert the connection as `active`, mark the request used, audit `provider_connection.create`, then `303` to
     `returnTo` or `/connect/done?provider=…&result=connected|missing_scopes`. The done page lists any required scope
     the provider didn't grant, so a renamed scope can't loop silently.
- **Open redirects:** `returnTo` must be a same-origin path: it starts with one `/`, has no `//`, `\` or scheme, and is
  at most 2000 characters (`400 invalid_return_to`). The callback only redirects to that path or to fixed web-app
  routes. The authorization URL is built from stored settings only.
- **Proactive connect:** "Connect" on Account › My connections (default scopes) and on a My gateways capability
  (`requires.scopes`) call start without a request.

## Refresh, status and revocation (decision 29)

- Refresh happens on demand before forwarding (step 4). There's no background refresh in this phase. A rotated
  refresh token replaces the stored one in the same transaction.
- `needs_reconsent` erases the tokens but keeps the row, scopes and label, so the person sees what to reconnect.
- Disconnect (Account › My connections, or a platform admin on the person's page) erases the tokens and deletes the
  row. Where `revocationUrl` exists, it then calls RFC 7009 with the refresh token (else the access token) on a
  best-effort basis. The audit records `providerRevoked` (`true`, `false` or `not_supported`).
- A suspended person keeps their connections; their requests are refused at authentication as today. Leaving a team
  changes nothing, because connections are personal.

## Security

- Tokens and the client secret are **never** returned, logged, audited, recorded in calls or health, or put into
  errors. Token-endpoint error bodies are reduced to an error code. A sentinel test covers responses, audit,
  `mcp_calls` and logs.
- A token is sent only to the registered URL of a server whose effective requirement for **that capability** names
  **that provider**. It is never sent on lists, discovery, test, refresh or health checks, and never to another server.
- Consent links are single-person, expire after 10 minutes, are used once, and need that person's browser session (the
  MCP spec's phishing guidance). The confirm page always shows the scopes before the provider is contacted.
- CSRF: start is a CSRF-protected POST from a browser session, and the callback state is bound to that session.
- The gateway doesn't check that the provider account belongs to the person (for example, by matching email).
  `accountLabel` is informational. Servers that care can check it with the provider.
- Server authors: verify the JWT first, check that its `provider` claim equals `MCP-Gateway-Provider`, and use the
  token only with that provider's API. [upstream-identity.md](upstream-identity.md) gets a "Provider tokens" section.

## Audit (decision 30)

- `auth_provider.create/update/delete` (target `auth_provider`; before/after without the secret; `clientSecretChanged`,
  `connectionsReset`, `connectionCount`).
- `provider_connection.create` (`scopes`, `reconnect`, `viaRequest`), `provider_connection.refresh_failed` (system
  actor; `reason`), and `provider_connection.revoke` (`reason` `user`/`admin`/`provider_deleted`, `providerRevoked`).
  These use target `provider_connection` with label "person · provider".
- `mcp_capability.requirement_set`; `mcp_server.update` carries the requirement before and after.

## UI

- **Admin › Auth providers:** list (name, preset icon, enabled, servers, connections); create (pick a preset, which
  pre-fills the fields and asks for its URL parameters; the redirect URI is shown with a copy button); record with
  Overview, Settings (secret "keep") and Delete (lists the servers when `in_use`).
- **Admin › Users › person › Connections:** metadata and Disconnect (auditors read only).
- **Team › Servers › server:** a "User authorization" section in Settings with a provider picker and scope chips. A
  warning says when saving sends the server back to review. The Capabilities tab has a requirement column and an
  override dialog. Admin review shows both.
- **Gateway pickers, Capabilities tab and catalog:** a "Needs GitHub · repo" badge, plus "Service accounts can't use
  this" in team and service-account views. **My gateways › gateway:** "Connected" or a Connect button per
  capability.
- **Account › My connections:** provider, account, scopes, status, last used, expiry; Connect, Reconnect, Disconnect.
- **`/connect/{provider}`** confirm page and **`/connect/done`** result page (connected, denied, missing scopes,
  expired, wrong person).

## Tests that must exist

- Authz matrix for every new operation; secrets and tokens never appear in responses, audit, `mcp_calls` or logs (sentinel).
- Provider validation: URL rules through the guard (private, blocked, http), reserved parameters, `none` without
  PKCE, `in_use` delete, clientId change resets connections, turned-off provider refused.
- Requirement rules: overrides, reset on provider change and removal, widening vs narrowing under both policies.
- Runtime against a fake provider and `cmd/fakemcp`: `-32042` shape and message link, request reuse and cap; the retry
  after consent forwards both headers and the `provider` claim; nothing is sent without a requirement or with
  `none`; missing scopes and the union request; refresh before expiry, rotation, concurrent refresh on two replicas,
  `invalid_grant` leading to `needs_reconsent` and `-32042`, a provider outage returning `-32010` without forwarding;
  service account and disabled provider refused; calls recorded with `providerSlug`.
- Connect flow: state single use and expiry; another session or person refused; another person's request is 404, an
  expired one 410; `returnTo` open-redirect cases; provider `error`; mix-up (state of provider A on B's callback);
  form-encoded and Slack responses; userinfo failure tolerated; revocation called and failures recorded.
- Reserved header names refused on servers. Vitest for every new screen, including the confirm page's scope list
  and wrong-person state.

## Not in this phase

Team-owned providers; non-OAuth per-person secrets (API keys); credentials for service accounts (for example,
client credentials); several accounts per provider; a server asking for re-consent after the provider answers 401;
`notifications/elicitation/complete`; background refresh; token exchange (RFC 8693) for internal APIs; matching the
provider account to the person.

## Build notes

Until the backend serves the new routes, `TestRoutesMatchOpenAPI` ("documented but not served") and the authz-matrix
classification test fail, and `make check-generated` reports stale types. Build the backend first, then run
`make generate`. New required response fields (`requirement`, `requirementOverride`, `requires`) need web fixtures
updated. A test fake OAuth provider (httptest, and `cmd/fakemcp -oauth` for local use) issues expiring and rotating
tokens.
