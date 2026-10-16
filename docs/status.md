# Status and handoff

Last updated: 2026-10-16.

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
  against JWKS, an unselected tool is refused as unknown and logged `denied`). UI: 219 Vitest tests, no browser QA yet. See
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
  - Gaps: resources; OAuth protected-resource metadata (Phase 4); no rate limit on `/mcp`; no SSE relay (calls are
    buffered within 1 MiB); `server/discover` (2026-07-28 stateless protocol) answered "method not found".

## Differences from Grounded (deliberate)

- OIDC login state lives in PostgreSQL (`login_states`), not Valkey.
- Rate limits are in-memory per process for now (shared PostgreSQL counters planned in Phase 6).
- Listen address env var is `LISTEN_ADDR` (default `:8088`); config file env var is `OMG_CONFIG_FILE`.
- Malformed emails return `400 invalid_json` (generated Email type), same as Grounded.
- Dropped for now: notifications/email, OAuth server (Phase 4), metrics/tracing, retention job. SSO groups are back
  (`users.groups`, for grants); API keys are our own implementation (`internal/apikeys`), personal only.

## Resume checklist

```sh
cd ~/projects/golang/open-mcp-gateway
make deps-up                  # Postgres on 127.0.0.1:55442
cp -n .env.example .env       # DEV_AUTH=true, APP_URL=http://localhost:8088, ENCRYPTION_KEY
make fake-mcp                 # fake MCP server on http://127.0.0.1:8091/mcp (separate terminal)
make web && make build        # embed UI
make run                      # http://localhost:8088, sign in as admin@localhost
make test && make web-test
```

## Next steps

1. Browser QA of the Phase 3 UI (and the Phase 2 fixes at 390px).
2. Phase 3 follow-ups: archived teams' gateways keep serving (should stop); pre-publish the next signing key before
   it signs; `/v1/me` doesn't return the person's groups; `gateway-options` has no paging past 1000; resources;
   API keys can't be scoped to gateways.
3. `make docker`; Kubernetes manifests later (Phase 6).
4. Phase 4: OAuth for MCP clients (protected-resource metadata at the URL `/mcp` already advertises), service accounts.
5. Open questions to settle: what happens to pending requests when the default policy changes; an admin
   health-checks route (the admin Health tab reads the team endpoint).

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
