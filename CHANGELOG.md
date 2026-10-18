# Changelog

Notable changes to Open MCP Portal, one entry per release. Design notes for each phase are in [`docs/`](docs/), and
[`docs/status.md`](docs/status.md) records what was verified.

The project follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html). Before 1.0, minor releases may include
breaking changes; the entry says how to adapt.

## Unreleased (0.2.0)

**MCP 2026-07-28, server side** ([docs/mcp-protocol.md](docs/mcp-protocol.md), decision 51). Gateways at `/mcp/{slug}`
now speak the new protocol era next to 2024-11-05 to 2025-11-25, chosen per request; legacy clients see no change.

- `server/discover` (supported versions, capabilities, identity); required `_meta`, `MCP-Protocol-Version`,
  `Mcp-Method` and `Mcp-Name` validated against the body (`400`, `HeaderMismatch` `-32020`; unsupported version
  `-32022` with the supported list; unknown methods `404` `-32601`); `Mcp-Param-*` headers validated for tools with
  `x-mcp-header`.
- Results carry `resultType` and `_meta` `serverInfo`; lists, discovery and reads carry `ttlMs` and
  `cacheScope: "private"` (lists differ per person, never public); lists sorted by exposed name.
- Any JSON Schema `outputSchema` and any JSON `structuredContent` pass through to modern clients (legacy clients
  still get object schemas only); resource not found is `-32602` (legacy `-32002`); `ping` and `logging/setLevel`
  are method not found; the rate-limit code is `429` instead of `-32029` for modern requests.
- "Connect your account" as a multi round-trip request: modern clients that declare URL elicitation get an
  `input_required` result with the existing connect link and a sealed `requestState` (AES-GCM, key derived from
  `ENCRYPTION_KEY`; bound to the person, gateway, request and parameters; 10 minutes) and retry; declines return
  "authorization declined". Everyone else keeps `-32042`.
- Dynamic client registration accepts and echoes `application_type` (`web` or `native`); documented as deprecated.
- `cmd/fakemcp -schema-tools`; `secrets.Box.Derive`. The MCP conformance suite was run against a gateway
  (results in docs/mcp-protocol.md).

## [0.1.0] - 2026-10-17

The first release ([release notes](docs/releases/v0.1.0.md)): a self-hosted portal where teams register MCP servers, publish them to a company catalog, curate
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

**Release** (v0.1.0). A `v*` tag runs `.github/workflows/image.yml`: it builds `ghcr.io/ncecere/open-mcp-portal` for
linux/amd64 and linux/arm64 on native runners with SBOM and provenance attestations, checks that the image reports the
tag's version and scans it, signs the multi-arch index with cosign (keyless) and tags it `vX.Y.Z` (and `latest` for a
final release). `scripts/github-release.sh` then creates the GitHub Release from `docs/releases/vX.Y.Z.md` with each
platform's SBOM, the digest and `checksums.txt`. `open-mcp-portal version` prints `open-mcp-portal 0.1.0 (<commit>)`. The
Kustomize base points at `ghcr.io/ncecere/open-mcp-portal:v0.1.0`.

[0.1.0]: https://github.com/ncecere/open-mcp-portal/releases/tag/v0.1.0
