# Roadmap

Status: **approved 2026-10-17**; v0.2 released as v0.2.0 on 2026-10-18 (a real deployment with Authentik sign-in, database backups and a tested restore was done on one cluster on 2026-10-18; real-client testing on the new revision is partly done: VS Code and Cursor are still open). It is an order of work, not a schedule.
Sizes are relative effort: **S** a few days, **M** a week or two, **L** several weeks. Each item that needs a design
gets one in `docs/` before code, as the earlier phases did. Decisions behind it are 47–58 in
[decisions.md](decisions.md).

## What the portal is, and isn't

Open MCP Portal is a portal for MCP servers: teams register them, a catalog lists them, gateways curate them, and
people and agents get governed access. It is **not** an LLM gateway, an agent runtime, a workflow engine or a RAG
system, and the roadmap adds none of those.

Decided out of scope:

- **Hosting stdio servers: never** (decision 47). Servers are remote and speak Streamable HTTP. A team with a stdio
  server runs a bridge next to it and registers the bridge.
- **Sampling, roots and logging.** The 2026-07-28 revision deprecates them; new work doesn't add them.
- **The old HTTP+SSE transport** for upstream servers: deprecated by the same revision.
- **Content scanning** (prompt-injection and data-loss checks on tool arguments and results): wanted, **not
  scheduled** (decision 50). See "Later".

## Why the order

1. **The protocol moved.** MCP 2026-07-28 is stateless: no `initialize`, per-request metadata, required request
   headers, cacheable lists, and multi-round-trip requests instead of server-initiated calls. The portal only
   speaks the earlier revisions, and the new era is already arriving: GitHub's hosted MCP server enforces the new
   parameter headers, and pi's client probes `server/discover` on our gateways before falling back.
2. **A team is about to try it.** What they hit first decides whether it lands: an overview page, notifications,
   automation tokens, and a real deployment.
3. **Governance builds on the protocol work.** Approvals before sensitive calls need the new multi-round-trip result,
   so they follow it.

## v0.2: MCP 2026-07-28 and the team pilot

### A. Speak both protocol eras (L)

**Status:** the server side (`/mcp/{slug}`, consent as a multi-round-trip request, authorization alignment, and the
MCP conformance suite run against a gateway) and the upstream side (era negotiation, relaying elicitation, streaming,
trace context, client authorization alignment, the conformance suite's client scenarios) are **done**:
[mcp-protocol.md](mcp-protocol.md). The SDK matrix with real clients is still open.

The portal keeps serving 2025-11-25 and earlier clients exactly as today and adds the 2026-07-28 era, chosen **per
request** (decision 51). The spec's own compatibility rule applies: a request carrying the new `_meta` and
`MCP-Protocol-Version: 2026-07-28` is modern; `initialize` means legacy.

**As the server your clients talk to** (`/mcp/{slug}`) — *done*. (Before this, `server/discover` answered "method not
found" and clients fell back to `initialize`.)

| Change | Notes |
| --- | --- |
| `server/discover` | Supported versions, capabilities, identity. |
| Request validation | `MCP-Protocol-Version`, `Mcp-Method` and `Mcp-Name` must match the body; a mismatch is `400` with `HeaderMismatch` (`-32020`). An unsupported version is `400` with `UnsupportedProtocolVersion` listing what we support. `Mcp-Name` is the *exposed* name (`server__tool`); calls to your server use the original. Tools marked `x-mcp-header` get their `Mcp-Param-*` headers validated too. |
| Result shape | `resultType: "complete"`; our identity in `_meta.serverInfo`. List results carry `ttlMs` and **`cacheScope: "private"`**: lists differ per person, so they must never be cacheable by shared intermediaries. Tools stay in a deterministic order. |
| Era-aware schemas | The new revision allows any JSON Schema 2020-12 in `inputSchema`/`outputSchema` and any JSON value as `structuredContent`. We currently strip non-object ones for strict older clients (GitHub's server sends arrays). Keep stripping for legacy; pass through for modern. |
| Error codes by era | Resource-not-found is `-32602` in the new era, `-32002` before. |
| Not in the new era | `ping`, `logging/setLevel`, session ids: ignored or `405`, as the spec says. |

**"Connect your account" becomes native.** The new revision's URL-mode elicitation *is* the flow we built: the call
returns an `InputRequiredResult` with a URL and an integrity-protected `requestState`; the person completes it
out of band; the client retries. For clients that declare URL elicitation, the consent link is returned that way,
with `requestState` bound to the person, a short expiry and a digest of the request (the spec's own advice). Clients
that don't declare it, and all legacy clients, keep today's `-32042` error with the link in the message. The
connect page's "the person who opens the link must be the person who asked" check already matches the spec's
phishing guidance.

**As the client talking to your MCP servers** — *done*. (Before this, the upstream client had multi-round-trip support
switched off and declared no capabilities.)

- Negotiate the era per server (`server/discover` first, `initialize` fallback) and make it visible on the server
  page. (S)
- **Relay elicitation.** When an upstream returns `input_required`, pass the elicitation to the client and map the
  retry back, wrapping the upstream's `requestState` in our own protected state (bound to person, gateway,
  upstream and request). This closes today's "no elicitation passthrough" limit. Sampling and roots requests are
  refused with a clear error. (M)
- **Streaming.** Return SSE for `tools/call` so `notifications/progress` flows and long calls don't buffer to 1 MiB;
  closing the stream cancels the upstream call; set `X-Accel-Buffering: no`; document ingress timeouts. (M)
- Send `traceparent`/`tracestate`/`baggage` in `_meta` in both directions (pairs with tracing in v0.3). (S)

**Authorization alignment (S)** — *done*. Accept `application_type` in dynamic registration; prefer Client ID Metadata
Documents and mark dynamic registration as deprecated in the docs (the revision deprecates it); key an upstream's
client credentials by issuer and re-register when the authorization server changes (SEP-2352); keep the `iss`
check.

**Proof (M).** Run the MCP project's conformance tests for both eras if they cover this revision, otherwise our own
test per spec requirement; run the Go, TypeScript and Python SDK clients in both modes against a gateway; keep
Claude Code, pi, VS Code and Cursor as a written compatibility matrix, tried for real.

### B. Pilot readiness

The first three items are **designed** together (decisions 56–58; they share `api/openapi.yaml`, the authz matrix and migrations `00014` and `00015`); the contract is in `api/openapi.yaml`, the implementation is next.

| Item | Size | Notes |
| --- | --- | --- |
| Admin overview | S | **Designed:** [admin-overview.md](admin-overview.md). `/admin` redirects to Users today. Show failing servers, pending reviews, changed capabilities, expiring keys, providers needing re-consent; plus a team attention card. |
| Notifications | M | **Designed:** [notifications.md](notifications.md). In-app inbox, email (SMTP) and signed webhooks (works for Slack). Server unhealthy, definition changed, review requested or decided, connection needs re-consent, key expiring. Per-person preferences; team and platform destinations. |
| Automation tokens | M | **Designed:** [automation-tokens.md](automation-tokens.md). The management API accepts browser sessions only. Add **team publisher tokens** (register, refresh, publish a team's servers from CI) and **platform-admin tokens**, each scoped, expiring, audited, and never valid for `/mcp`. |
| Real deployment | M | Your cluster, Authentik OIDC, a tested backup and restore, the example overlay with the real v0.1.0 digest, and a feedback channel for the pilot. **Status:** done on one cluster on 2026-10-18 (Authentik sign-in, a CloudNativePG database with WAL archiving and daily backups to object storage, a restore tested with a point-in-time replay, the overlay pinned to v0.2.0's digest); the feedback channel is still open. |
| Threat model | S | Written for security reviewers, as the sister projects have. Include how forwarded provider tokens relate to the spec's "no token passthrough" rule. |
| Housekeeping | S | Dependabot PRs; browser re-checks of the Phase 4–7 screens. |

## v0.3: credentials and tool policy

Making "all MCP servers" true, then governing what runs.

**Upstream credentials (L).**
- **Per-person secrets.** Many servers want each person's own API key or token, not OAuth. A server auth mode where
  each person pastes theirs once (sealed, revocable), sent as a header, bearer or query parameter.
- **More static shapes:** several headers, query-string keys, Basic auth. **Machine OAuth:** client credentials.
  **mTLS** and a per-server CA.
- **Service connections.** A team-owned shared connection (a provider token or an upstream sign-in) that service
  accounts can use, so agents and CI can call tools that today need a person.

**Tool policy and scoping (L, decisions 48–49).**
- **Classification.** Server owners mark each tool read, write or destructive. Annotations from the server prefill it
  but are never trusted.
- **Scopes on credentials.** API keys, OAuth grants, service accounts and gateway grants can be limited to a class or
  to named tools ("read-only key"). The consent screen shows the scope; the OAuth `scope` maps to it.
- **Argument constraints and curation.** Per gateway tool: fixed or allowed values (pin GitHub's `owner` to your
  organisation), the schema shown to the model narrowed to match, renamed tools, rewritten descriptions.
- **Approval before sensitive calls.** A rule on a tool or gateway ("destructive tools need approval") returns the
  multi-round-trip result with an approval link; named approvers approve in the portal; the person retries. Approvals
  expire, are single-use, bound to the arguments and audited, with self-approval rules and notifications. Legacy
  clients get the error-with-link form. This is a portal feature, not a workflow engine.
- **Access requests.** People ask for a gateway; its owner approves.

**Reliability and observability (M).**
- Per-server quotas and concurrency caps (limits today are per person, credential, service account and gateway);
  retries for reads, circuit breaker, several upstream URLs per server with failover.
- OpenTelemetry tracing, Grafana dashboards, Prometheus alert rules.
- The tasks extension (`io.modelcontextprotocol/tasks`) for long-running tools, with handles bound to the person who
  started them.

## v0.4: identity lifecycle, compliance and scale

- **SCIM 2.0 and SSO-group to team-role mapping (L).** Leavers lose access automatically; deprovisioning revokes
  sessions, keys, connected apps and connections. Session list, revoke and idle timeout.
- **Audit and data (M).** Stream the audit log to a SIEM (signed webhook, syslog/TLS, OTLP) with gap detection;
  export the call log; legal holds; per-person data export and deletion.
- **Server governance (M).** Owner and on-call contact, data-sensitivity label with ceilings, re-review dates,
  deprecation and sunset, owner transfer.
- **Secrets and keys (M).** OpenBao/Vault/KMS references for `ENCRYPTION_KEY` and upstream secrets, envelope
  encryption.
- **Operate at scale (L).** Helm chart, autoscaling, HA PostgreSQL guidance, backup job and restore runbook, a
  multi-replica load test, a kind smoke test and an upgrade test in CI.
- **`subscriptions/listen` (M).** List-changed notifications over the new long-lived stream.

## v0.5: developer experience and catalog

- **CLI (`omp`) (M):** sign in, print connect snippets, manage keys, register and publish servers from CI,
  export and import configuration.
- **Configuration as code (M):** a GitHub Action to register or refresh a server on deploy, and a Terraform provider.
- **Try-a-tool console (M):** call a tool from the browser as yourself and see the exact request and response, for
  server authors.
- **Catalog quality (M):** categories and tags, icons, a README per server, a verified badge, search across tools,
  popularity from usage.
- **Tool search (M):** for large catalogs, a gateway option that exposes "find tool" and "call tool" instead of
  hundreds of definitions in the model's context.
- **Chargeback reports (S):** usage per team and per gateway, exported.

## Later

- **Content scanning** (decision 50): prompt-injection checks on tool descriptions and results, and data-loss
  detection on arguments and results. Design it first; OMG's guardrails design is the reference.
- Multiple identity providers (contractors); mTLS for clients; DPoP-bound tokens.

## Still to verify from v0.1.0

Real Google and Microsoft providers for delegated credentials (only the fake and GitHub's hosted server have run);
VS Code and Cursor over OAuth; the Kubernetes manifests on a real cluster; end-to-end redaction of provider tokens in
recorded contents. These are items in v0.2 B and the compatibility matrix above.

## Resolved questions (2026-10-17)

1. **Suspension blocks** and does not disconnect (decision 53).
2. **Tool scoping** means limiting which tools a credential can use and constraining arguments (decision 49, confirmed).
3. **Who approves:** the server's team owners and admins by default, named approvers per rule, never the requester,
   platform admins as the fallback (decision 52).
4. **Oldest protocol:** keep every revision accepted today and add 2026-07-28; negotiation prefers the newest
   (decision 54).
5. **Automation tokens:** two kinds, platform-admin and team publisher (decision 55).
