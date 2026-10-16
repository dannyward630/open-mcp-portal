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

## Differences from Grounded (deliberate)

- OIDC login state lives in PostgreSQL (`login_states`), not Valkey.
- Rate limits are in-memory per process for now (shared PostgreSQL counters planned in Phase 6).
- Listen address env var is `LISTEN_ADDR` (default `:8088`); config file env var is `OMG_CONFIG_FILE`.
- Malformed emails return `400 invalid_json` (generated Email type), same as Grounded.
- Dropped for now: notifications/email, SSO group mapping (needed again for Phase 3 grants), API keys and OAuth
  server (Phase 4), metrics/tracing, retention job.

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

1. Phase 3 (gateways): design drafted in [phase3-gateways.md](phase3-gateways.md); **answer its 5 open questions**,
   then add the contract to `api/openapi.yaml` and build. Phases 1–2 committed as f518e6b.
2. Quick browser re-check of the Phase 2 fixes at 390px.
3. `make docker`; Kubernetes manifests later (Phase 6).
4. Phase 3: gateways, `/mcp/{slug}`, identity JWT + JWKS, call metadata. Needs SSO group claims back for
   group grants.
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
