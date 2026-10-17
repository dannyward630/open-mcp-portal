# Status and handoff

Last updated: 2026-10-16 (Phase 5 backend built: [phase5-delegated-credentials.md](phase5-delegated-credentials.md)
"As built"; before that, Phase 5 designed, decisions 25–30).

## Where things stand

- Requirements interview: done. Decisions in [decisions.md](decisions.md); architecture, data model and phases in
  [../SPEC.md](../SPEC.md).
- **Phase 1 (foundations): done.** Browser QA found one blocking bug (`name@localhost` rejected) and polish items; fixed
  in the Phase 2 UI pass except: unnamed command-palette listbox (in `components/ui`), admin team tabs wrapping and
  breadcrumb collapse gap on mobile.
  - Go backend: config, store, migrations (`00001_identity`, `00002_teams`), OIDC + dev sign-in, sessions,
    users, teams, members, invites, platform admin, audit, all routes in `api/openapi.yaml`.
  - Tests: unit + integration (per-test databases), route/OpenAPI contract test, authz matrix over every operation.
  - Verified: `go build`, `go vet`, `gofmt`, `go test ./...`, integration tests with `-race`, `serve` smoke test.

- **Phase 2 (registry): done.** Browser QA run end to end; its findings are fixed (URL change now re-reads
  capabilities and blocks approval until read; form error clearing; rejected → private; wording/confirmations).
  Not re-checked in a browser after the fixes: the 390px layout tweaks. See [registry.md](registry.md).
  - UI routes: `/teams/$team/servers` (+ `?form=new`), `/teams/$team/servers/$serverId` (Overview, Capabilities,
    Health, Settings), `/catalog`, `/catalog/$serverId`, `/admin/servers`, `/admin/servers/$serverId`,
    `/admin/publishing`, publishing override in admin team › Settings. Shared record code in
    `web/src/components/servers/`. 171 Vitest tests.
  - Migration `00003_registry` adds `mcp_servers`, `mcp_capabilities`, `mcp_health_checks`, a one-row `platform_settings` table and `teams.publish_policy_override`.
  - New packages: `internal/registry`, `internal/upstream` (guarded client and SSRF guard), `internal/secrets`, plus `cmd/fakemcp` (`make fake-mcp`).
  - New config: `ENCRYPTION_KEY` (required by `serve`; the example key is refused off loopback), `ENCRYPTION_KEY_PREVIOUS` and `HEALTH_CHECK_INTERVAL` (default 5m). **An existing `.env` needs the `ENCRYPTION_KEY` line from `.env.example`.**
  - Every Phase 2 route in `api/openapi.yaml` is served, with no contract changes. The authz matrix covers every new operation. The tests cover discovery against a fake server, change detection, both publishing policies, catalog visibility and header-value non-disclosure.
  - Gaps: there is no bulk `rotate-keys` command. A URL change on a published server goes back to review under approval_required.

- **Phase 3 (gateways): backend and UI done; verified end to end with curl** (team server → gateway with 2 of 5
  tools → API key → `initialize`, `tools/list` shows only the 2, `whoami` reaches the fake with a JWT it verified
  against JWKS, an unselected tool is refused as unknown and logged `denied`). Browser QA done (no security or blocking bugs); its findings are fixed. 235 Vitest tests. See
  [phase3-gateways.md](phase3-gateways.md) "As built" and [upstream-identity.md](upstream-identity.md).
  - Migration `00004_gateways` adds `gateways`, `gateway_capabilities`, `gateway_grants`, `api_keys`, `signing_keys`,
    `mcp_calls` (append-only) and `users.groups`.
  - New packages: `internal/gateways`, `internal/mcpgw` (`/mcp/{slug}`), `internal/identity` (ES256 JWT, JWKS at
    `/.well-known/jwks.json`, rotation loop), `internal/apikeys`, `internal/calls`. `internal/upstream` gained
    `CallTool`/`GetPrompt` and an identity header; `cmd/fakemcp` gained `whoami`/`echo` and `-jwks`.
  - New config: `API_KEY_PEPPER` (required by `serve`; the example value is refused off loopback) and
    `OIDC_GROUPS_CLAIM` (default `groups`). **An existing `.env` needs the `API_KEY_PEPPER` line from `.env.example`.**
    Dev personas sign in with sample groups (engineering, support, …) for group grants.
  - Every Phase 3 route in `api/openapi.yaml` is served with no contract changes; the contract test skips `/mcp/` and
    `/.well-known/`. The authz matrix covers every new operation. Integration tests cover selection rules, grants by
    team/user/group, `/v1/me/gateways` reasons, an SDK-client end-to-end run (JWT verified against the JWKS, aud =
    server URL, never the API key), nine refusals between list and call that never reach the server, key rotation, and
    a sentinel check that arguments/results are absent from `mcp_calls`, audit and logs.
  - Gaps: resource subscriptions; no rate limit on `/mcp`; no SSE relay (calls are
    buffered within 1 MiB); `server/discover` (2026-07-28 stateless protocol) answered "method not found".
  - Follow-ups done (not yet browser-checked): archived teams stop serving (their team gateways answer 404, their
    servers leave the catalog, gateway-options and health loop, other gateways show `team_archived`; unarchive
    restores); signing keys are pre-published (`next` state, `SIGNING_KEY_PREPUBLISH`, default 24h; migration
    `00005_signing_key_next`); `/v1/me` returns `groups` (Account › Profile shows them; My gateways names the granting
    group via `MyGateway.groups`); `GET /v1/admin/mcp-servers/{serverId}/health-checks` (Admin's Health tab uses it);
    policy changes leave pending servers pending (documented in [registry.md](registry.md), tested).
    Contract changes: `GatewayUnavailableReason` + `team_archived`, `Me.groups` (required), `MyGateway.groups`
    (optional), the admin health-checks operation.
  - Follow-ups done (not yet browser-checked): gateway-options paged (`GatewayOptionPage`, `cursor`/`limit`
    100–500/`q`/`kind`/`server`; the picker searches on the server, filters by kind and server, loads more);
    resources and resource templates (exposed as `omcp://{serverSlug}/{uri}`, RFC 6570 `{var}`/`{+var}` templates,
    `resources/list|templates/list|read`, JWT claim `resource`); API keys limited to gateways (`gatewayIds`,
    `ApiKey.gateways`, error class `key_scope`). Migration `00006_gateway_resources_key_scopes`. See
    [phase3-gateways.md](phase3-gateways.md) "As built".

- **Internal MCP servers (decision 20): done, not browser-checked.** Platform admins allow internal networks (CIDRs)
  and host names (exact or `*.suffix`, optionally with plain http) in **Admin › Internal networks**
  (`/admin/networks`: staged edits, Save with If-Match and confirmation, read-only deployment entries, a URL checker);
  `UPSTREAM_ALLOWED_NETWORKS` adds read-only deployment entries. Loopback, link-local/metadata, unspecified, multicast
  and broadcast stay blocked. Rules in [registry.md](registry.md#security), operations in
  [deploy.md](deploy.md#internal-mcp-servers).
  - Migration `00007_network_allowlist` adds a one-row `network_settings` table (entries JSON, own revision).
  - New package `internal/netpolicy` (classification, canonical entries, matching); `upstream.Client` gained
    `Networks` (the live policy) and `Dial` (a test seam); `registry.Networks` caches the admin entries (30s; the saving
    replica applies a change at once).
  - Contract changes: `GET`/`PUT /v1/admin/settings/network`, `POST /v1/admin/settings/network/check`; schemas
    `NetworkEntry`, `NetworkSettings`, `NetworkSettingsUpdate`, `NetworkCheckRequest`, `NetworkCheckResult`; new
    `url_not_allowed` reason `blocked_address` (loopback/link-local/metadata literals and `localhost`, previously
    `private_address`); `private_address` now says a platform admin can allow the network; audit
    `settings.network_update`. `Registry.Target` takes a context.
  - UI: the add-server and settings URL fields let plain http through to the server (it decides from the allowlist);
    a `private_address` refusal links platform admins to Internal networks and tells others to ask one.

- **Phase 4 (client sign-in and service accounts): backend and UI done; QA follow-ups done.** OAuth 2.1 authorization
  server for `/mcp/{slug}` (always on; CIMD and DCR clients, PKCE S256, resource = one gateway, consent per person +
  client + gateway, trusted clients, rotating refresh tokens) and team service accounts with `omcp_sa_` keys. See
  [phase4-client-auth.md](phase4-client-auth.md) "As built", [connect-clients.md](connect-clients.md) and
  [upstream-identity.md](upstream-identity.md) (`principal_type`).
  - Migration `00008_client_auth` adds `oauth_settings` (trusted clients, seeded with Claude, Claude Code, VS Code),
    `oauth_clients`, `oauth_grants`, `oauth_codes`, `oauth_tokens`, `service_accounts`, `service_account_keys`,
    `service_account_key_gateways`; `mcp_calls` gains principal kinds `oauth`/`service_account` (nullable `user_id`)
    and snapshot columns.
  - New packages: `internal/oauth`, `internal/serviceaccounts`, `internal/ratelimit`. `upstream.Client.GuardedDial`
    lets metadata documents use the MCP servers' address guard and allowlist. `auth.SessionUser`/`LoginURL`; the
    post-login `next` limit grew to 8300 characters (an authorization request).
  - Protocol endpoints `/oauth/*` and `/.well-known/oauth-*` are outside the OpenAPI contract (the contract test skips
    `/oauth/`). Every `/v1` route already in `api/openapi.yaml` is served with **no contract changes**; the authz
    matrix covers all 16 new operations.
  - Verified: `make generate`, gofmt, vet, build, `go test ./...`, `make test-integration` (race), and a curl smoke
    test (fake MCP server, CIMD on a loopback http server under the development allowance, hand-made PKCE pair,
    consent, token, `tools/call whoami` showing `principal_type: user` and `client`, refresh rotation, then a service
    account key showing `principal_type: service_account`). Not browser-checked.
  - Gaps: no rate limit on `/mcp` itself; rate counters per process; no previous-pepper support (rotating
    `API_KEY_PEPPER` signs clients out and invalidates keys); no CORS on OAuth endpoints; no private-use redirect
    schemes; DCR clean-up runs on registration; the previous release can't list call records of service accounts.
  - QA follow-ups done (not yet browser-checked): see [phase4-client-auth.md](phase4-client-auth.md) "As built: QA
    follow-ups". Contract changes: `GET /v1/teams/{team}/service-accounts/{serviceAccountId}/gateways`
    (`MyGateway` items), `GET /v1/admin/service-accounts` (`ServiceAccountPage`), team call lists also include the
    team's service accounts' calls through any gateway, `CallPrincipal.serviceAccountExists` and
    `oauthClientVerified`; identity JWT claim `client_verified`. Migration `00009_call_principals` (nullable
    `mcp_calls.oauth_client_kind`, an index). CIMD fetches logged at info level; the OAuth error page follows the
    color mode; UI fixes (rotation note, sign-in note for apps, combined notices, wording, focus).

- **Phase 5 (delegated credentials): backend done.** Auth providers (presets for GitHub, Google, Microsoft Entra ID,
  Atlassian, Slack, GitLab), server requirements and capability overrides (review when widened), consent links
  (JSON-RPC `-32042`), the connect flow (`POST /v1/me/connections/start`, `GET /connect/callback`), tokens sealed and
  refreshed under a row lock, revocation, and the runtime headers `MCP-Gateway-Provider`/`MCP-Gateway-Provider-Token` with
  the identity claim `provider`. See [phase5-delegated-credentials.md](phase5-delegated-credentials.md) "As built" and
  [upstream-identity.md](upstream-identity.md#provider-tokens).
  - Migration `00010_delegated_credentials` (`auth_providers`, `provider_connections`, `provider_connect_requests`,
    `provider_auth_states`; requirement columns on `mcp_servers`/`mcp_capabilities`; `mcp_calls.provider_slug` and the
    outcome `needs_authorization`).
  - New package `internal/providers`; `cmd/fakeoauth` (`make fake-oauth`, port 8092); the fake MCP server's
    `provider_token` tool. No new settings.
  - Contract: all 14 operations served. Additive changes made while building: `AuthProviderPreset.redirectUri`, `name`
    in `in_use` details' servers, provider slugs `done` and `callback` reserved (`422 invalid_slug`).
  - Verified: `make generate`, gofmt, vet, build, `go test ./...`, `make test-integration` (race), `make k8s-validate`.
    Not browser-checked; not tried against a real provider.
  - Gaps: the start rate limit is per process; no background refresh; see the design's "Not in this phase".

- **Deployment (Phase 6 slice): done, not on a cluster.** Distroless image (`make docker`; `--target fakemcp` for the
  demo), `compose.demo.yaml` (`make demo`: gateway + Postgres + fake MCP server, dev sign-in), Kustomize base and
  example overlay in `deploy/kubernetes` (one Deployment with a migrate init container, Service, PDB, NetworkPolicies,
  ServiceAccount), `make k8s-validate` (kustomize + kubeconform). Guide: [deploy.md](deploy.md). Verified: image
  `migrate` + `serve` in production mode (https APP_URL, OIDC, fresh keys) against a scratch database, `/healthz`,
  `/readyz`, `doctor`, SIGTERM drain; demo discovered the fake server's capabilities; `k8s-validate` passes.

## Differences from Grounded (deliberate)

- OIDC login state lives in PostgreSQL (`login_states`), not Valkey.
- Rate limits are in-memory per process for now (shared PostgreSQL counters planned in Phase 6).
- Listen address env var is `LISTEN_ADDR` (default `:8088`); config file env var is `OMG_CONFIG_FILE`.
- Malformed emails return `400 invalid_json` (generated Email type), same as Grounded.
- OAuth for MCP clients is always on (Grounded has a setting) and binds each connection to one gateway.
- Dropped for now: notifications/email, metrics/tracing, retention job. SSO groups are back
  (`users.groups`, for grants); API keys are our own implementation (`internal/apikeys`), personal only.

## Resume checklist

```sh
cd ~/projects/golang/open-mcp-gateway
make deps-up                  # Postgres on 127.0.0.1:55442
cp -n .env.example .env       # DEV_AUTH=true, APP_URL=http://localhost:8088, ENCRYPTION_KEY
make fake-mcp                 # fake MCP server on http://127.0.0.1:8091/mcp (separate terminal)
make fake-oauth               # fake OAuth provider on http://127.0.0.1:8092 (delegated credentials)
make dev-seed                 # after make run: team demo, published server, gateways demo + company
make web && make build        # embed UI
make run                      # http://localhost:8088, sign in as admin@localhost
make test && make web-test
```

## Next steps

1. Browser re-check of the Phase 4 QA follow-ups (390px service account header, Admin › Calls filter, dark error page)
   and QA of the OAuth flow with real clients (Claude Code, VS Code, Cursor) against a reachable deployment.
2. Phase 5 (delegated credentials): **backend built**; the web UI is built in parallel from the same contract. Then a
   browser QA pass of the connect flow (`/connect/{provider}`, `/connect/done`) with `make fake-oauth`, and a check
   against one real provider (GitHub is the simplest).
3. Deployment follow-ups: kind smoke test (`k8s-smoke`), image release workflow, metrics; see [deploy.md](deploy.md)
   "Not covered yet".

## Phase 1 UI

- Grounded's Vite/React app, Bitop UI library unchanged, templates, shell, team switcher, admin mode, command palette.
- Routes: `/`, `/settings/profile`, `/teams/$team`, `/teams/$team/settings?tab=members|audit|general`,
  `/admin` (→ users), `/admin/users`, `/admin/users/$userId`, `/admin/teams`, `/admin/teams/$team`, `/admin/audit`.
- Navigation lives in `web/src/components/layout/nav.tsx` as typed lists; a test checks links resolve. Adding MCP
  sections = a route plus one nav item.
- Verified: typecheck, 118 Vitest tests (with axe), build, styling/size checks; embedded build serves `/` and deep
  links; auditor can read team audit and admin users.
- `npm install` crashed in npm's resolver; the lock file was derived from Grounded's and pruned (256 packages, 0
  vulnerabilities). Re-check with a clean `npm ci`.

Known UI gaps:

- No team-scoped "update team" endpoint: team settings › General saves through `PATCH /v1/admin/teams/{team}`, so
  only platform admins can rename/archive (same as Grounded).
- No admin overview endpoint; `/admin` redirects to Users.
- `web/README.md` missing; `src/components/templates/README.md` and some comments still use Grounded wording.
- `errors.tsx` and `pager.tsx` copied but unused.
- Playwright e2e suite not set up.
