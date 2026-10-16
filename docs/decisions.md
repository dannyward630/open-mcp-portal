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

## Proposed build order

1. Foundations from Grounded: config, store, OIDC/dev sign-in, sessions, users, teams, platform admin, audit, UI shell.
2. Upstream server registry with discovery of tools, resources and prompts.
3. Gateways, access policy and the `/mcp/{slug}` endpoint, with signed identity on every upstream call.
4. Client authentication for MCP clients (OAuth for Claude Code, VS Code and similar; API keys for headless agents).
5. Auth providers, per-user token vault and the "authorize this tool" flow.
6. Usage, rate limits, health and execution history.

## Open questions

