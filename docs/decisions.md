# Product decisions

Status: requirements interview in progress. Nothing below is implemented yet.

## Purpose

A central, self-hosted place for the company to discover and access its many MCP servers. It is closer to
Arcade than to the MCP features in LiteLLM, Bifrost or the Docker MCP Gateway: the gateway brokers who the
user is and what each tool may do on their behalf, not only network routing.

Not in scope: hosting or running agents (that is the separate `open-agent-platform` idea).

## Confirmed

| # | Area | Decision |
|---|---|---|
| 1 | Location | `~/projects/golang/open-mcp-gateway` |
| 2 | Stack | Go backend, PostgreSQL, React/TypeScript UI, following Grounded (net/http, pgx/sqlc/goose, OpenAPI-generated types, Bitop UI, TanStack Router/Query) |
| 3 | Identity and roles | Grounded's model: OIDC sign-in, platform roles `platform_admin`/`platform_auditor`, teams with `owner`/`admin`/`editor`/`member`, users, invites, audit |
| 4 | UI | Looks like Grounded and Open Model Gateway: separate platform admin and team portals, list/detail/settings templates |
| 5 | Client endpoints | Authenticated Streamable HTTP MCP endpoints that show each caller only the tools, resources and prompts they are allowed to use |
| 6 | Gateways | Curated collections like Arcade: many gateways, each at `/mcp/{slug}`, mixing selected tools from several MCP servers |
| 7 | Identity to upstream servers | Every forwarded call carries a short-lived JWT signed by the gateway (user, teams, gateway, tool); servers verify it against the gateway's published keys |
| 8 | Delegated credentials | Tools that declare an auth requirement also get the user's brokered OAuth token (GitHub, Google, Atlassian, internal APIs). Built after signed identity |
| 9 | Server registration | Teams register and own their MCP servers, private to the team by default. Publishing to the company-wide catalog follows a platform policy: `approval_required` (default, first release) or `self_publish` (later). Platform admins can override the policy per team, e.g. let a trusted team self-publish |
| 10 | Upstream transports | First release: remote servers only, reached over Streamable HTTP; teams deploy their own servers. Later: gateway-hosted stdio servers in containers. Keep transport behind an interface so hosting can be added without changing gateways, policy or the registry |
| 11 | Deployment | Like Grounded: Docker Compose for local development and small installs, Kubernetes (Kustomize) for production. One stateless Go binary that can run several replicas; PostgreSQL is the only required dependency (no Valkey at first: rate counters and tokens live in PostgreSQL) |
| 12 | Client authentication | Both: OAuth sign-in for interactive MCP clients (Claude Code, Claude Desktop, VS Code, Cursor) through the company IdP, and API keys (personal or team service-account) for headless agents, CI and scripts. Adapt Grounded's `internal/oauth` (PKCE S256, Client ID Metadata Documents, dynamic registration, consent page) and API keys; add an allowlist of trusted client IDs that skip consent |
| 13 | Gateways and access | Team gateways (team admins curate from their own servers plus catalog servers) and platform gateways (platform admins curate, shared with selected teams, users or SSO groups). Personal gateways later. A gateway exposes only the tools selected for it; access is granted to teams, users or SSO groups; a tool can require a minimum team role. Every call checks gateway tool list, caller access, server publish status and (later) the user's OAuth consent |
| 14 | Call records | Metadata always (caller, gateway, tool, server, outcome, latency, sizes). Argument/result capture is opt-in per server or gateway, truncated and redacted, with a retention period (platform default, team overrides, as in Grounded) and visible only to the owning team's admins and platform auditors |
| 15 | Phase 3 client credential | Personal API keys ship in Phase 3 (our own implementation; Grounded is only a reference). OAuth for MCP clients and service accounts stay in Phase 4 |
| 16 | Team gateway editing | Team owners and admins create, edit and delete team gateways (a gateway grants access); members read them and connect |
| 17 | Sharing team gateways | Team gateways are for the team's members only; cross-team sharing goes through platform gateways |
| 18 | Resources | Phase 3 exposes tools and prompts; resources and resource templates follow in a later step |
| 19 | Identity header | `Authorization: Bearer <jwt>` when the server has no static `Authorization` header; otherwise `MCP-Gateway-Identity: <jwt>` |
| 20 | Internal MCP servers | Platform admins maintain an allowlist of internal networks (CIDRs) and hostnames (exact or `*.suffix`) that MCP servers may use, each optionally allowing plain `http`. A deployment setting can add entries (read-only in the UI). Loopback, link-local (incl. cloud metadata), unspecified and multicast addresses stay blocked whatever the list says; everything not listed stays blocked as today |
| 21 | OAuth connections | One connection per gateway: an OAuth token is bound to one gateway (`resource` = its URL); connecting a client to several gateways means approving each, unless the client is trusted |
| 22 | Service account roles | A service account has a team role of `member` or `editor` (used for minimum-role checks); never owner or admin |
| 23 | Service accounts and platform gateways | Service accounts reach platform gateways only through grants to their team; no grants to individual service accounts |
| 24 | Trusted clients | The trusted-client allowlist (decision 12) starts with Claude, Claude Code and VS Code; platform admins can edit it |
| 25 | Auth providers *(accepted default)* | Platform admins configure auth providers: generic OAuth 2.0 authorization code with PKCE where the provider supports it, client ID, client secret (sealed with `ENCRYPTION_KEY`, never returned), authorization, token and optional revocation and userinfo endpoints, default scopes, extra authorize parameters. Presets (GitHub, Google, Microsoft Entra ID, Atlassian, Slack, GitLab) only pre-fill the form. Provider endpoints are dialled through the SSRF guard and the internal-network allowlist. No team-owned providers for now |
| 26 | Requirements *(accepted default)* | A server's team (editors and above) sets an optional requirement on the MCP server (provider + scopes) and optional per-capability overrides (none, or other scopes of the same provider). One provider per call. Requirements are part of review: under `approval_required`, a change to a published server's requirement that widens what is sent goes back to review, like a URL change |
| 27 | Delivery *(accepted default)* | For a call that needs a provider, the gateway sends the person's access token in `MCP-Gateway-Provider-Token` and the provider's slug in `MCP-Gateway-Provider`, alongside the identity JWT (decision 19 unchanged). The JWT gains a `provider` claim when a token is attached. Servers and capabilities without a requirement never get either |
| 28 | Missing consent *(accepted default)* | The call is not forwarded. The gateway returns JSON-RPC `-32042` (`URLElicitationRequiredError`, MCP 2025-11-25) with one URL elicitation to `<APP_URL>/connect/{provider}?request=<opaque id>`; the error message also carries the URL. The link works only in that person's browser session, expires after 10 minutes and is used once; afterwards the person retries the call |
| 29 | Tokens *(accepted default)* | One connection per person and provider: tokens sealed at rest, refreshed by the gateway before expiry, union of granted scopes tracked. A failed refresh marks the connection as needing re-consent. People see and revoke theirs on Account › My connections; platform admins see metadata and can revoke anyone's (auditors read). Suspended people keep connections, but their calls are refused as today. Service accounts can't use capabilities that require a provider (a clear error); those capabilities stay listed, with a note in management views |
| 30 | Audit *(accepted default)* | `auth_provider.create/update/delete` and `provider_connection.create/refresh_failed/revoke`; never a token or secret |

Decisions 25–30 are defaults the assistant accepted under the user's instruction "continue until both are done".
They are not interview answers, and the user may change them. Design: [phase5-delegated-credentials.md](phase5-delegated-credentials.md).

## Proposed build order

1. Foundations from Grounded: config, store, OIDC/dev sign-in, sessions, users, teams, platform admin, audit, UI shell.
2. Upstream server registry with discovery of tools, resources and prompts.
3. Gateways, access policy and the `/mcp/{slug}` endpoint, with signed identity on every upstream call.
4. Client authentication for MCP clients (OAuth for Claude Code, VS Code and similar; API keys for headless agents).
5. Auth providers, per-user token vault and the "authorize this tool" flow.
6. Usage, rate limits, health and execution history.

## Open questions

