# Changelog

Notable changes to Open MCP Portal, one entry per release. Design notes for each phase are in [`docs/`](docs/), and
[`docs/status.md`](docs/status.md) records what was verified.

The project follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html). Before 1.0, minor releases may include
breaking changes; the entry says how to adapt.

## [0.1.0] - unreleased

The first version: a self-hosted portal where teams register MCP servers, publish them to a company catalog, curate
gateways and give people and agents governed access. Migrations `00001` to `00013`.

**Foundations** (Phase 1). Configuration from environment variables or a YAML file (`OMP_CONFIG_FILE`, `<NAME>_FILE`
secrets), PostgreSQL 17 with forward-only expand/contract migrations, OIDC sign-in plus a loopback-only development
sign-in, sessions with CSRF and Origin checks, users, teams, members and invites, platform admins and auditors, an
append-only audit log, and the web UI shell (workspace and admin modes, team switcher, command palette). The OpenAPI
contract (`api/openapi.yaml`) is enforced by a route test and an authorization matrix over every operation.

**Registry and catalog** (Phase 2). Team-owned remote MCP servers with discovery of tools, resources, resource
templates and prompts, change detection and review, scheduled health checks, encrypted static header values
(`ENCRYPTION_KEY`), a company catalog with approval or self-publishing policies, and an SSRF guard. Platform admins
allow internal networks and hosts (decision 20, `UPSTREAM_ALLOWED_NETWORKS`).

**Gateways** (Phase 3). Curated sets of capabilities served at `/mcp/{slug}` (Streamable HTTP, stateless), granted to
teams, people or SSO groups; tools and prompts exposed as `{server}__{name}`, resources as `omp://{server}/{uri}`.
Every upstream call carries a short-lived ES256 identity JWT (JWKS at `/.well-known/jwks.json`, rotating and
pre-published keys). Personal API keys (`omp_`, HMAC digests with `API_KEY_PEPPER`), optionally limited to some
gateways. A call log without arguments or results.

**Sign-in for MCP clients and service accounts** (Phase 4). An OAuth 2.1 authorization server for `/mcp/{slug}`:
PKCE S256, Client ID Metadata Documents and dynamic registration, one gateway per connection, per-person consent,
trusted clients, rotating refresh tokens (`ompat_`, `omprt_`, `ompac_`, `ompcl_`). Team service accounts with
expiring keys (`omp_sa_`, two active for rotation).

**Delegated credentials** (Phase 5). Auth providers (presets for GitHub, Google, Microsoft Entra ID, Atlassian, Slack
and GitLab), server requirements and capability overrides, consent links (JSON-RPC `-32042`), each person's tokens
sealed, refreshed under a row lock and revocable, sent to servers in `MCP-Gateway-Provider-Token`. The Gmail example
server in `examples/mail-mcp`.

**Operations** (Phase 6). Rate limits shared by every replica (per person, credential, service account and gateway),
opt-in payload capture (redacted, truncated, sealed, audited views), retention, hourly usage rollups and reports,
Prometheus metrics (`omp_*`, `METRICS_ADDR`), pepper and encryption key rotation (`open-mcp-portal rotate-keys`),
`doctor`, a distroless image, a demo Compose file, a Kustomize base and overlay, and CI.

**Per-person OAuth to upstream servers** (Phase 7). Servers in `oauth` mode (such as GitHub's hosted MCP server):
sign-in discovery (RFC 9728, RFC 8414, OIDC), DCR, CIMD, a manual client or a linked platform provider, a setup
sign-in that reads capabilities, each caller's own token with refresh and step-up, and a per-server `sendIdentity`
switch.

**Fixes since Phase 7**, found by running Claude Code, pi and GitHub's hosted MCP server end to end. An empty token scope from a provider (GitHub Apps) counts as no scope information; tool lists
drop `outputSchema` values that aren't objects and relayed results drop `structuredContent` that isn't an object (MCP
schema); client cancellations aren't recorded as errors; failed exchanges with a server include the server's own
reason (redacted, bounded); forwarded tool calls send SEP-2243 `Mcp-Param-*` headers built from the approved
definition (GitHub's server needs them).

**Renamed to Open MCP Portal** (decision 46), formerly Open MCP Gateway, so it no longer clashes with Open Model
Gateway. Module `github.com/ncecere/open-mcp-portal`, binary `open-mcp-portal`, `OMP_*` environment variables, `omp_*`
metrics, `open-mcp-portal` Kubernetes names. Keys and tokens now start with `omp_`, `omp_sa_`, `ompat_`, `omprt_`,
`ompac_` and `ompcl_` (were `omcp_`, `omcp_sa_`, `omat_`, `omrt_`, `omac_`, `omcl_`), and exposed resources with
`omp://` (was `omcp://`): keys and tokens made before no longer authenticate; create new keys and sign MCP clients in
again. Payload capture still redacts the old prefixes. Migration `00013_open_mcp_portal`. The `MCP-Gateway-*` headers
MCP servers receive keep their names. A development database of the old Compose project moves with
`scripts/migrate-dev-db.sh`.

[0.1.0]: https://github.com/ncecere/open-mcp-portal/commits/main
