# Phase 4 — Client sign-in (OAuth) and service accounts (design draft)

Status: **draft for review, not implemented**. Builds on decisions 12, 15–17. Items marked *proposed* need a yes/no.
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

## Open questions

1. **One connection per gateway** (a person connecting Claude Code to three gateways approves three times, unless
   the client is trusted)? *Recommended: yes* — it matches how MCP clients add servers.
2. **Service account roles** limited to `member`/`editor`? *Recommended: yes.*
3. **Service account access to platform gateways via team grants only** (no individual grants)? *Recommended: yes.*
4. **Trusted clients seeded** with Claude, Claude Code and VS Code (platform admins can remove them)? *Recommended: yes.*
