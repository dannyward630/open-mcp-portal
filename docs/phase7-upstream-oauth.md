# Phase 7: Per-user OAuth to OAuth-protected MCP servers (design)

Status: **designed, not built.** Decisions 39–44 in [decisions.md](decisions.md) are defaults the assistant chose because
the user asked to implement now. The contract is in [`../api/openapi.yaml`](../api/openapi.yaml): `McpServer.authMode`/`oauth`,
`detectTeamMcpServerAuth`, `authorizeTeamMcpServerSetup`, and `ProviderRef.kind`/`server`. Client-side reference:
[MCP authorization 2025-11-25](https://modelcontextprotocol.io/specification/2025-11-25/basic/authorization) (RFC 9728,
RFC 8414/OIDC discovery, PKCE S256, RFC 8707 `resource`, CIMD, RFC 7591). First real target: GitHub's remote server
`https://api.githubcopilot.com/mcp/`.

## Goal

Some MCP servers are OAuth resource servers themselves. GitHub's remote server is one example: it accepts only a
GitHub token issued to the person. The gateway acts as an **MCP client** to such a server. A team registers the URL, the
gateway discovers how the server signs people in, and each person signs in once. From then on, every call carries
that person's own token. The model and the MCP client never see it.

## How it fits Phase 5

Phase 5 sends a *third-party* token next to the call (`MCP-Gateway-Provider-Token`). In Phase 7 the token *is* the
server's credential. The machinery is the same, so this phase reuses it.

| Reused unchanged (Phase 5 as built) | New in Phase 7 |
|---|---|
| `provider_connections` (one per person and provider, tokens sealed, `needs_reconsent`), refresh under `SELECT … FOR UPDATE`, RFC 7009 revocation, guarded token exchanges (`internal/providers/endpoint.go`) | A **server-bound provider** row (kind `mcp_server`) holds the server's client and discovered metadata |
| Consent links: JSON-RPC `-32042`, `/connect/{slug}?request=`, `GET /v1/connect/requests/{id}`, start, `/connect/callback`, `/connect/done`, session-bound state | Discovery (`internal/upstream/oauthmeta`): challenge parsing, protected-resource and authorization server metadata, DCR/CIMD |
| Account › My connections, Admin › Users › Connections, `provider_connection.*` audit | `resource` on authorize/token/refresh, the `iss` check, 401/403 handling on calls, the health probe |

## Data model (migration `00012_upstream_oauth`, expand-only)

| Table | Change |
|---|---|
| `mcp_servers` (+) | `auth_mode` `none`/`static_header`/`oauth` (backfilled: `static_header` where a header exists); `oauth_setup_user_id` (FK users, `SET NULL`), `oauth_setup_at`. CHECK: `oauth` ⇒ no `auth_provider_id` and no static `Authorization` header (other static headers stay allowed) |
| `auth_providers` (±) | `kind` adds `mcp_server`; `server_id` (unique, FK `mcp_servers`, set iff kind `mcp_server`); `link_provider_id` (FK `RESTRICT`, for `platform_provider`); `registration` `dcr`/`cimd`/`manual`/`platform_provider`; `resource`, `resource_metadata_url`, `authorization_server`, `issuer`, `scopes_supported`, `send_resource`, `iss_param_supported`, `client_secret_expires_at`, `discovered_at`. `client_id` becomes nullable only for `platform_provider`. `default_scopes` holds the server's scopes. `name` follows the server's name. `slug` is `mcp-` + 12 random hex characters, and new platform providers can't use the `mcp-` prefix (`422 invalid_slug`, reserved) |
| `provider_auth_states` (+) | `setup_server_id` (authorize-setup), `resource` (a snapshot: the callback refuses when the server's resource changed in between) |
| `mcp_calls` | `provider_slug` = the server-bound slug; new error classes `upstream_unauthorized`, `insufficient_scope` |

Platform lists (`GET /v1/auth-providers`, Admin › Auth providers, the 100-provider limit) cover only `kind = oauth2`.
Deleting a server first revokes its connections (best effort), then deletes the server, its provider row and its
connections, as a Phase 5 provider delete does. `rotate-keys` already covers the columns.

## Roles

Editors and above detect, configure, sign in for setup, test and refresh. Members and platform staff read
`McpServer.oauth`, which never holds a secret. Configuring a client is an exception to decision 25: the client belongs
to the server, not to the platform. Platform admins review the setup as part of a server's review. Each person manages
their own connection; platform admins can see and revoke anyone's.

## Setup by discovery (decision 41)

**Detect** (`POST …/mcp-servers/detect-auth {url}`). It changes nothing, isn't audited and is limited to 10 per minute
per person. Every request goes through the address guard and the allowlist: no proxy, no redirects, 5 s each,
responses of at most 64 KiB, at most 8 requests.

1. Send an unauthenticated `initialize`. A success means `no_auth`. A 401 with `Bearer` is parsed for
   `resource_metadata`, `scope` and `error`. A 401 or 403 without usable OAuth metadata means `other_auth` (use a static
   header). Network errors and 5xx answers mean `unreachable`.
2. Read the protected-resource metadata from `resource_metadata`. Without that parameter, try
   `/.well-known/oauth-protected-resource{path}`, then the root form. `resource` must equal the URL. When it differs
   only by a trailing slash or the case of the scheme or host, the result offers `suggestedUrl` (GitHub's resource is
   `…/mcp/`). Any other difference is the blocking problem `resource_mismatch`.
3. Use the first of `authorization_servers`, or the one chosen. Read its metadata in the spec's order: for an issuer
   with a path, `/.well-known/oauth-authorization-server{path}`, `/.well-known/openid-configuration{path}`, then
   `{issuer}/.well-known/openid-configuration`; without a path, the two root forms. GitHub serves only the first:
   `https://github.com/.well-known/oauth-authorization-server/login/oauth`. `issuer` must equal the identifier
   (`issuer_mismatch`). `S256` must be in `code_challenge_methods_supported` (`no_pkce`; the spec says to refuse).
   Endpoints must be https and allowed by the guard.
4. Registration options: `cimd` when the metadata has `client_id_metadata_document_supported` and `APP_URL` is public
   https; `dcr` when there is a `registration_endpoint`; `platform_provider` for each enabled platform provider whose
   `authorizationUrl` and `tokenUrl` equal the discovered endpoints (platform providers have no issuer field, so
   endpoints are compared); and always `manual`. The recommendation follows the spec's order (CIMD, then DCR), else
   manual. Suggested scopes are the challenge's `scope`, else `scopes_supported`. The result also lists every request
   made (`steps`) and the redirect URI.

**Save** (`POST`/`PATCH` with `authMode: oauth`). The gateway runs the discovery again itself and never trusts
endpoints sent by the browser. For DCR it registers before the transaction (RFC 7591): `client_name` "<instance> ·
<server>", `redirect_uris` [`<APP_URL>/connect/callback`], `grant_types` `authorization_code refresh_token`, and
`token_endpoint_auth_method` `client_secret_basic`/`client_secret_post` when listed, else `none`. RFC 7592 management
isn't used. For CIMD the client ID is `<APP_URL>/oauth/upstream-client-metadata.json`, a public client the gateway
serves outside the contract. Secrets are sealed with the AAD `auth_provider:<id>`. In oauth mode a new server's first
capability discovery is skipped (`setupRequired: true`).

**Setup sign-in** (`POST …/{serverId}/oauth/authorize-setup`). This is the Phase 5 start flow for the caller's own
connection, using the server's scopes. After the callback, that connection becomes the **setup connection**
(`mcp_server.auth_update`, `setupBy`) and capabilities are read with it at once. It counts only while its person is an
active editor or above of the team; otherwise its status is `missing`. Any editor can sign in again as themselves.

```text
Editor (browser)             Gateway                              api.githubcopilot.com     github.com
 │ URL + Detect ────────────►│ POST /mcp/ initialize (no token) ──────►│
 │                           │◄─ 401 Bearer resource_metadata=".../oauth-protected-resource/mcp/"
 │                           │ GET PRM ───────────────────────────────►│ resource, authorization_servers, scopes_supported
 │                           │ GET /.well-known/oauth-authorization-server/login/oauth ───────────►│ issuer, endpoints, S256
 │◄─ "GitHub (github.com/login/oauth). No automatic registration: create an OAuth app with this callback"
 │ (creates a GitHub OAuth App, callback <APP_URL>/connect/callback)
 │ Save: manual, client ID + secret, scopes ─►│ discover again, seal secret, server-bound provider, audit
 │ "Sign in to read capabilities" ───────────►│ authorize-setup → authorizeUrl (state, S256, scope, resource)
 │──────────────────────────────────── authorize ───────────────────────────────────────────────────►│ consent
 │◄────────────────────────── /connect/callback?code&state&iss ──────────────────────────────────────│
 │                           │ state + session; iss == issuer; code → token (guarded, resource) ──►│
 │                           │ seal; setup connection; initialize + lists with Bearer ►│
 │◄─ 303 server page: "Capabilities read as you"
```

With DCR or CIMD, the editor only detects, chooses "automatic" and saves; the rest is the same.

## Runtime (decisions 39, 43)

The existing checks run first. Lists are served from approved definitions and never reach the server. For
`tools/call`, `prompts/get` and `resources/read` on an `oauth` server:

1. A service account gets `-32010` (`service_account`), recorded `denied`. A linked platform provider that has been
   turned off gets `-32010` (`provider_disabled`).
2. With no active connection, or granted scopes that don't cover `oauth.scopes`, the gateway answers `-32042` with a
   consent link `/connect/mcp-…?request=…`. The message reads "github__get_me needs you to sign in to GitHub MCP
   (repo …)". This is the Phase 5 shape, reuse and cap.
3. A token that expires within 60 s is refreshed under the row lock, with `resource` included, as in Phase 5.
4. The call is forwarded to the stored URL, which must equal `oauth.resource`. Otherwise it is refused and nothing is
   sent. Headers: `Authorization: Bearer <token>` and `MCP-Gateway-Identity: <jwt>`. There is no
   `MCP-Gateway-Provider*` header and no `provider` claim. `last_used_at` is updated as in Phase 5.
5. **Upstream 401** (`Bearer`, `invalid_token` or no error). If there is a refresh token and this call hasn't
   refreshed yet, refresh once and retry once; a 401 means the server didn't execute the call. Otherwise erase the
   tokens, mark the connection `needs_reconsent`, audit `provider_connection.refresh_failed` (`upstream_rejected`) and
   answer `-32042`. The call is recorded `needs_authorization`, class `upstream_unauthorized`.
6. **Upstream 403 `insufficient_scope`** with `scope`. This is a step-up: open a request for granted ∪ challenged
   scopes (`missing_scopes`) and answer `-32042`. If the person already holds every challenged scope, or a scope is
   outside the reviewed `scopes` ∪ `scopesSupported`, answer `-32010` (`insufficient_scope`) instead. That stops
   loops, as the spec requires.

The upstream may show each person different tools (GitHub filters by token scopes). The gateway lists what the setup
connection discovered and approved; the upstream still checks every call.

## Health (decision 42)

- **Scheduled** checks use nobody's token. An unauthenticated `initialize` that gets a 401 `Bearer` challenge whose
  `resource_metadata` equals the stored URL (or has none, when the metadata was found through the fallback) counts as
  healthy with `signInRequired: true`, shown as "requires sign-in". A challenge pointing somewhere else fails with class
  `auth`. A 2xx answer is healthy, with the message "answered without sign-in".
- **Manual test** and **refresh** use the setup connection, refreshing it if needed. If that connection has lapsed,
  the test fails (`setupRequired`) and refresh answers `409 setup_reconsent_required`. The server shows "Capabilities
  read as Ana · needs sign-in again".

## Review (decision 40)

Admin review shows the auth mode, authorization server (host highlighted when it differs from the server's host),
issuer, registration, scopes and `scopesSupported`. Under `approval_required`, a published server goes back to
`pending` (`mcp_server.publish_request`, reason `upstream_auth_changed`) on any of these: a switch to `oauth`, another
authorization server, or a scope that wasn't asked for before. A URL change does so as today. Narrowing scopes,
changing the client or leaving `oauth` does not. A URL change, another authorization server or another client ID
erases every connection's tokens (`needs_reconsent`, `connectionsReset`). Leaving `oauth` deletes the connections and
revokes them where possible. Capability overrides answer `409 upstream_oauth`.

## Security

- **Token audience.** `resource` is the server's canonical URI on authorize, token and refresh requests (MCP spec).
  `sendResource: false` is a per-server escape hatch for authorization servers that reject it, shown to reviewers.
- **No confused deputy.** A server connection's token goes only to that server's stored URL (equal to `resource`, and
  checked before every send), to its token endpoint (refresh) and to its revocation endpoint. It never goes on lists,
  to another server or into `MCP-Gateway-Provider-Token`. A platform connection's token is never sent as `Bearer`.
  Linking a platform provider shares its *client* (ID, secret, endpoints), never its connections: every server has its
  own connections and its own `resource`.
- **Metadata checks.** The protected-resource `resource` must match the URL, the authorization server's `issuer` must
  match its identifier, PKCE S256 is required, and every endpoint is https and guarded. A `resource_metadata` URL from
  a 401 is untrusted input: guarded, size-limited, and accepted only when its `resource` matches.
- **Mix-up and CSRF.** The state is bound to the session, person, server-bound provider and resource. `iss` must equal
  `issuer` when the server advertises `authorization_response_iss_parameter_supported` (GitHub does), and must match
  whenever it is present. A mismatch exchanges nothing (`/connect/done?result=error`). Authorization URLs are built
  from stored metadata only.
- **Secrets.** Client secrets and tokens are never returned, logged, audited, put in call records or health checks,
  or captured. Payload capture never stores headers, and `call_secrets` adds the upstream token for exact-value
  redaction. A sentinel test covers all of these.
- The connect confirm page names the server, the authorization server's host and every scope ("lets GitHub MCP act
  as you at github.com").

## Audit (decision 44)

`mcp_server.auth_update` (target `mcp_server`; mode, authorization server, registration, scopes and `sendResource`
before and after; `clientIdChanged`, `clientSecretChanged`, `registered`, `connectionsReset`, `setupBy`). The
`provider_connection.create/refresh_failed/revoke` events are reused with the label "person · server name" and the
revoke reasons `server_auth_changed` and `server_deleted`. A token never appears in the audit.

## UI

- **Team › Servers › Add server / Settings › Authentication:** the mode (None, Static header, Sign-in with OAuth) and
  **Detect sign-in**, which shows what was found (authorization server, scopes, PKCE, registration) and what is still
  needed. "Use …/mcp/" applies `suggestedUrl`. Registration offers Automatic, Use platform provider X, or Enter a
  client (client ID, secret, token method, with the redirect URI and a copy button). Scope chips come from
  `scopesSupported`, with a review warning. After saving: "Sign in to read capabilities".
- **Server › Overview:** "Sign-in: GitHub (github.com/login/oauth) · Capabilities read as Ana · 12 people signed in",
  and Re-authorize as me. **Health:** "Healthy · requires sign-in". **Admin › Servers:** an `authMode` filter and a
  review card.
- **Catalog, picker, My gateways:** "Each person signs in to GitHub MCP"; Connect per server. **My connections:**
  server entries with an "MCP server" badge and the team. The **connect pages** are unchanged except for the wording.

## Tests that must exist

- **Fakes.** `cmd/fakemcp -protect <issuer>` (also `testutil`) answers 401 with `resource_metadata`, serves the
  protected-resource metadata, and accepts only bearer tokens from `fakeoauth` whose bound resource is its own URL
  (checked with `fakeoauth`'s new `/introspect`). A `write` tool answers 403 `insufficient_scope`, and `whoami`
  reports a token hash, never the token. `cmd/fakeoauth` gains `-issuer-path /login/oauth` (metadata only at the
  path-insertion URL, `iss` in responses), `-dcr` (`/register`), `-cimd` and `-no-dcr` (the GitHub case). It records
  `resource` and refuses it with `-reject-resource`.
- **Detect:** every outcome; fallback URIs; path-insertion order; `suggestedUrl`; `resource_mismatch`;
  `issuer_mismatch`; `no_pkce`; guard refusals (private, metadata, redirect, size); request cap; no side effects.
- **Save:** DCR registration (secret sealed, method chosen), CIMD (`APP_URL` public vs loopback), manual, platform link
  (endpoint match, turned off, provider PATCH `409 in_use`), conflicts with the header and the requirement, discovery
  repeated on save, review triggers under both policies, and resets on URL, issuer and client changes.
- **Runtime:** `-32042` and retry; the bearer token only to its own server (a second protected server never sees it);
  the JWT moved to `MCP-Gateway-Identity`; refresh with `resource`; a 401 leading to a refresh and one retry, then
  `needs_reconsent`; 403 step-up, and refusal when already granted; service account refused; nothing sent on lists.
- **Callback:** a missing or wrong `iss`; mix-up (state from server A on B); a resource change during the flow;
  session binding (Phase 5 cases rerun for the server-bound kind).
- **Health and setup:** probe semantics; setup connection status (role lost, disconnected, lapsed → `409`).
- **Sentinel and authz:** tokens and secrets absent from responses, audit, calls, captured payloads and logs; the authz
  matrix for both new operations; Vitest for every new screen.

## Manual test plan: GitHub

1. **Create a GitHub OAuth App** (GitHub › Settings › Developer settings › OAuth Apps › New OAuth App). Homepage URL:
   `<APP_URL>`. Authorization callback URL: `<APP_URL>/connect/callback` (GitHub accepts `http://localhost:8088/…`
   for local runs). Copy the **Client ID**, then **Generate a new client secret** and copy it. For an organization
   with OAuth App access restrictions, an owner must approve the app. Variant: a GitHub App with "Expire user
   authorization tokens" on (8 h tokens with refresh) to exercise refresh. GitHub Apps ignore scopes; set
   permissions on the app instead.
2. In a team: **Add server**, URL `https://api.githubcopilot.com/mcp/`, then **Detect sign-in**. Expect OAuth, the
   authorization server `https://github.com/login/oauth`, S256, "no automatic registration", and ten supported scopes.
3. Choose **Enter a client** and paste the client ID and secret (token method `client_secret_post`). Scopes: start
   with `repo read:org read:user user:email`; the spec default preselects all ten, so remove the rest. Keep
   `sendResource` on. If GitHub refuses the authorization, turn it off and record the result here.
4. Save, then **Sign in to read capabilities**: approve on GitHub. The server shows its tools and "read as you".
   Publish it (or use a team gateway) and select `get_me` and an issue tool.
5. Connect Claude Code to the gateway as a second person and call `get_me`. Expect `-32042`; open the link, sign in,
   and retry. The result shows the second person's GitHub login.
6. Revoke the app on GitHub (Settings › Applications › Authorized OAuth Apps) and call again. Expect 401, then
   `needs_reconsent`, then a new link. Wait one health interval and expect "Healthy · requires sign-in".
7. Check the call records, the audit log and `My connections`. Run `grep -r gho_` over the logs: there must be no
   match.

## Not in this phase / open questions

Not in this phase: manual endpoints for servers without metadata; RFC 7592 client management; token exchange; client
credentials for service accounts; per-capability scopes on `oauth` servers; per-person tool lists; background refresh.

**Open question for the user:** in `oauth` mode the identity JWT (email, name, teams) still goes to the server
(decision 7). For a third party such as GitHub that is personal data leaving the company. A per-server switch to omit
it would change decision 7, so it is not designed here.

## Build notes

Until the backend serves `detectTeamMcpServerAuth` and `authorizeTeamMcpServerSetup`, `TestRoutesMatchOpenAPI` and the
authz-matrix classification fail; build the backend first, then run `make generate`. The new response fields are
optional (absent before Phase 7), so web fixtures stay valid. When this is built, update [registry.md](registry.md) and
the "Where the token arrives" table in [upstream-identity.md](upstream-identity.md).
