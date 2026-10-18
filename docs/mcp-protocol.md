# MCP protocol eras at `/mcp/{slug}`

A gateway speaks two protocol eras on the same URL (decision 51). The era is chosen **per request**; nothing is
remembered between requests. Written against MCP 2026-07-28 ([changelog](https://modelcontextprotocol.io/specification/2026-07-28/changelog))
and the earlier revisions 2024-11-05 to 2025-11-25. Code: `internal/mcpgw` (`era.go`, `modern.go`, `requeststate.go`).

| | Legacy (2024-11-05 to 2025-11-25) | Modern (2026-07-28) |
| --- | --- | --- |
| Opens with | `initialize` | nothing; `server/discover` is optional |
| Every request carries | nothing required | `_meta` `io.modelcontextprotocol/protocolVersion` and `…/clientCapabilities`; headers `MCP-Protocol-Version`, `Mcp-Method` and, for calls and reads, `Mcp-Name` |
| Results | as before | `resultType`, `_meta` `…/serverInfo`; lists and reads also `ttlMs` and `cacheScope` |
| `ping`, `logging/setLevel`, session ids | `ping` answers; no sessions either | removed |
| "Connect your account" | JSON-RPC `-32042` | `input_required` result (clients with URL elicitation) |

## Which era a request is

A request is **modern** when its `params._meta` carries a `protocolVersion` that is not a legacy version (so
`2026-07-28`, or something we don't have, which is then refused), or, with no such `_meta`, when its
`MCP-Protocol-Version` header is a date of 2026-07-28 or later. Everything else is **legacy**: `initialize` (whatever it
carries), notifications and responses (no id), and requests without those signs. So a bare `server/discover` (no `_meta`,
no header) is "method not found" as it always was, and old clients see no difference at all: no new header is required of
them, none is validated, and their results are byte for byte what they were (a relayed result keeps whatever fields the
upstream SDK put in it).

`GET` and `DELETE` are `405` in both eras. `Mcp-Session-Id` and `Last-Event-ID` are ignored (the gateway never mints a
session id). JSON-RPC batches are `400`; a foreign `Origin` is `403`; the body is capped at 1 MiB. Authentication is the
same in both eras (API key, OAuth token, service account key).

## Validation (modern requests only)

Checked before the request is looked at, in this order. Each refusal is a JSON-RPC error with the request's id and is
not recorded in the call log (the header check of a tool's `Mcp-Param-*` headers is, as `bad_request` / `header_mismatch`).

| Problem | HTTP | Code |
| --- | --- | --- |
| `_meta` lacks `protocolVersion` or `clientCapabilities` (or capabilities is not an object) | 400 | `-32602` |
| `MCP-Protocol-Version` missing, or different from `_meta` | 400 | `-32020` HeaderMismatch |
| Version not served (`1900-01-01`, `2027-…`) | 400 | `-32022` UnsupportedProtocolVersion, `data: {supported, requested}` |
| `Mcp-Method` missing or different from `method` | 400 | `-32020` |
| `Mcp-Name` missing or different from `params.name` (`tools/call`, `prompts/get`) or `params.uri` (`resources/read`) | 400 | `-32020` |
| `Mcp-Name` or `Mcp-Param-*` with characters a header can't carry, or invalid base64 | 400 | `-32020` |
| A tool with `x-mcp-header` arguments: a present argument without its `Mcp-Param-<Name>` header, a different value, or a header for an absent or null argument | 400 | `-32020` |
| A method we don't implement (including `ping`, `logging/setLevel`, `subscriptions/listen`, `resources/subscribe`, and `resources/*` on a gateway with no resources) | 404 | `-32601` |

`Mcp-Name` is the **exposed** name (`server__tool`, or the `omp://…` URI of a resource); calls to your server use the
original name, and its own `Mcp-Param-*` headers, built from the approved definition. The `Mcp-Name` and `Mcp-Param-*`
values may use the `=?base64?…?=` form; integers compare as numbers. Header names are case-insensitive. `supported` lists
`2026-07-28, 2025-11-25, 2025-06-18, 2025-03-26, 2024-11-05`.

Unknown tools, prompts and resources are as before for the gateway's own checks (refused like an unknown name,
recorded as denied): `-32602` for tools and prompts, and for a resource `-32602` (modern) or `-32002` (legacy).

## `server/discover`

Modern requests only. Needs the same authentication as everything else.

```json
{"resultType": "complete",
 "supportedVersions": ["2026-07-28", "2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"],
 "capabilities": {"tools": {"listChanged": false}, "prompts": {"listChanged": false},
                  "resources": {"listChanged": false, "subscribe": false}},
 "_meta": {"io.modelcontextprotocol/serverInfo": {"name": "<gateway slug>", "title": "<gateway name>", "version": "<build version>"}},
 "ttlMs": 30000, "cacheScope": "private"}
```

`resources` appears only when the gateway selects a resource or template the caller can see; there are no
subscriptions and no list-changed notifications. There are no `instructions` (the legacy `initialize` result has none).
The capabilities are the same as `initialize` would return.

## Results, caching and order

Every modern result has `resultType: "complete"` and our identity in `_meta["io.modelcontextprotocol/serverInfo"]`
(the same name, title and version as the legacy `serverInfo`; anything else a server put in `_meta` stays; the
server's own `serverInfo` is removed as before). `tools/list`, `prompts/list`, `resources/list`,
`resources/templates/list` and `server/discover` also carry:

- `ttlMs: 30000`, short because grants and selections change at any time and the portal sends no change
  notifications;
- **`cacheScope: "private"`, always.** What a person sees depends on their role and grants, so a shared cache must never
  serve it to someone else. (The HTTP response is also `Cache-Control: no-store`.) Every page of a list has the same
  scope.

`resources/read` results carry `ttlMs: 0` and `cacheScope: "private"`: the contents come live from your server, whose
volatility the portal doesn't know. Call and prompt results carry no cache hints. Lists are sorted by exposed name (a
resource's URI), so the order is stable across requests and pages (paged at 512 KiB with an opaque cursor, as before).

**Schemas.** The modern era allows any JSON Schema as `outputSchema` and any JSON value as `structuredContent`; the
gateway passes them through untouched. For legacy clients it still drops an `outputSchema` that isn't `"type": "object"`
(strict clients reject the whole list otherwise) and a `structuredContent` that isn't an object (GitHub's server returns
arrays; the same data is in the text content). A tool's `x-mcp-header` annotations are listed as approved.

## Consent as a multi round-trip request

Where a call needs your account on another service (a delegated provider, phase 5; a per-person sign-in to an
OAuth-protected server, phase 7), the answer depends on the client:

- **Modern request, `clientCapabilities.elicitation` has a `url` key** (an empty `elicitation: {}` means form only):
  HTTP 200 and

  ```json
  {"resultType": "input_required",
   "inputRequests": {"connect_<provider slug>": {"method": "elicitation/create",
       "params": {"mode": "url", "url": "<APP_URL>/connect/<provider>?request=…",
                  "message": "Connect your GitHub account (repo) to use github__create_issue in the Support gateway."}}},
   "requestState": "…",
   "_meta": {"io.modelcontextprotocol/serverInfo": {…}}}
  ```

  on `tools/call`, `prompts/get` and `resources/read` (the only methods the spec allows it on). The `url` is the link
  `-32042` carries (an opaque id: it signs nobody in), and the connect page's "the person who opens it must be the person
  who asked" check is unchanged. There is no `elicitationId` (removed in 2026-07-28). The message names the provider or
  the server's own sign-in, the scopes, the capability and the gateway, and says whether it is a first connection, more
  access or a reconnection. The call is recorded `needs_authorization` as before and nothing reaches your server.
- **Modern request without URL elicitation, and every legacy request:** the JSON-RPC error `-32042` with the link in
  `data.elicitations` and in the message, exactly as before. (2026-07-28 says implementations must not emit `-32042`;
  see "Deviations".)

The person opens the link, then the client **retries the original request** with `requestState` echoed and
`inputResponses`. The gateway verifies the state, then re-runs every normal check, so the person may be connected now
(it proceeds and answers normally, `resultType: "complete"`) or not yet (a new `input_required` with the same link and a
fresh state).

- `inputResponses` are ignored except for `action: "decline"` or `"cancel"` on a verified state: a tool call then
  returns the tool error `authorization declined` (`isError: true`), a prompt or read the refusal `-32010` with
  `data.reason: "authorization_declined"`. Nothing is forwarded; recorded `tool_error` / `denied` with error class
  `authorization_declined`. `inputResponses` that isn't an object is `-32602`.
- **`requestState`** is sealed with AES-256-GCM under a key derived by HKDF-SHA-256 from `ENCRYPTION_KEY` (and
  `ENCRYPTION_KEY_PREVIOUS`, so a key rotation doesn't strand someone mid-consent) with its own info string
  `open-mcp-portal/mcp-request-state/v1`, so the client can neither read nor change it. It binds a version byte, the
  **person**, the **gateway**, the **method**, the exposed **name or URI**, a SHA-256 **digest of the request's parameters**
  (`params` without `_meta`, `inputResponses` and `requestState`, canonical JSON, so a retry may change the JSON-RPC id,
  `_meta` and key order but not the arguments), and issue and expiry times (**10 minutes**). It holds no credential, no URL
  and nothing about the provider. A state that is altered, from another key, for another person, expired or for another
  request is refused with `-32602` and a message that says what to do (`requestState is not valid`, `has expired`,
  `was issued for a different request`; a state for another person is "not valid", so nothing is said about whose it
  was), recorded `bad_request` with error class `request_state_invalid`, `request_state_expired` or
  `request_state_mismatch`. It is not single use (a retry only re-runs the checks, so replaying one gains nothing) and
  is never logged.

## Authorization

Aligned with the 2026-07-28 authorization pages (diff of `internal/oauth` against them):

| Spec | State |
| --- | --- |
| Protected resource metadata (RFC 9728) with `authorization_servers`; `WWW-Authenticate: resource_metadata` on 401 | already done |
| Authorization server metadata (RFC 8414) at `/.well-known/oauth-authorization-server` | already done |
| Client ID Metadata Documents, advertised as `client_id_metadata_document_supported: true`; the client's `client_id` must equal the document's URL, redirect URIs validated | already done (preferred) |
| `iss` (RFC 9207) in authorization responses, errors included, with `authorization_response_iss_parameter_supported: true` | already done |
| Resource indicators (RFC 8707): `resource` required at `/oauth/authorize` and checked against the gateway; at `/oauth/token` optional but, when sent, must be the one the code or grant was issued for; tokens are accepted only by their own gateway | already done |
| PKCE S256 required; public clients only (`token_endpoint_auth_method: none`) | already done |
| `application_type` (`web` or `native`) in Dynamic Client Registration | **new**: accepted, validated (anything else is `invalid_client_metadata`), echoed in the response, recorded in the audit entry. Omitting it still works (pi doesn't send it). The portal is not an OpenID Provider, so it doesn't restrict redirect URIs by it |
| Dynamic Client Registration is deprecated, kept for compatibility | `/oauth/register` stays. The spec defines no metadata field that marks it deprecated, so the metadata says nothing; prefer metadata documents ([connect-clients.md](connect-clients.md)) |
| `scope` in `WWW-Authenticate`, `scopes_supported` | not applicable: gateway tokens carry no scopes (access is decided per call from the person's grants) |
| Client credentials bound to the issuer, re-registration when the authorization server changes (SEP-2352) | a client-side rule; for the portal as a *client* of upstream servers it is a v0.2 item not done here |

Claude Code (a metadata document) and pi (self-registration) are covered by tests in both eras
(`oauth_spec_2026_integration_test.go`).

## Limits and not done

- No SSE: every response is one JSON object and a call is buffered (≤ 1 MiB). No `subscriptions/listen`, no
  `notifications/progress`, no tasks extension.
- The gateway never originates input requests except consent. Upstream `input_required` results are not relayed (the
  upstream client has multi round-trip off).
- No `completion/complete`, logging, sampling or roots, and `ping` only for legacy clients.

## Deviations from the specification

- **`-32042` is still sent to modern clients that don't declare URL elicitation.** 2026-07-28 says implementations of it
  must not emit `-32042`; there is no other way to give such a client the link, and the message carries it. Change it
  (for example to `-32010` with a reason) if a strict client needs that.
- **Rate limit.** `-32029` (legacy) is inside the range reserved for the specification in 2026-07-28, so modern requests
  get code `429` (the HTTP status) instead. The status, `Retry-After`, and `data` are the same.
- **Refusals** keep our grandfathered codes `-32001` (HTTP-level refusals) and `-32010` (a refusal with a reason), in the
  legacy range the spec tolerates but discourages.
- **A dual-era server accepts `initialize`.** The conformance scenario that expects `initialize` to be "method not found"
  assumes a modern-only server (below).
- `Accept` is not required to list `text/event-stream` (we never stream).

## Conformance

The MCP project's suite (`@modelcontextprotocol/conformance` 0.2.0-alpha.12, `server --spec-version 2026-07-28 --suite
all`, plus `2025-11-25`) was run against a portal built from this branch on a scratch database, with a gateway over
`cmd/fakemcp` (`-schema-tools`) and a small proxy that adds the bearer key (the runner can't send one). Results:

- **Pass:** `caching` (8/8), `http-header-validation` (14/14), `http-custom-header-server-validation` (10/10, with the
  `lookup` tool), `sep-2164-resource-not-found`, `tools-list`, `resources-list`, `prompts-list`,
  `server-sse-multiple-streams`, `input-required-result-missing-input-response`, `-unsupported-methods`,
  `-ignore-extra-params`, `-validate-input`, `server-stateless` (20 of 25 checks), and `wire-schema-valid` everywhere.
  Legacy (`2025-11-25`): `server-initialize`, `ping`, `tools-list`, `resources-list`, `prompts-list`.
- **Can't pass by design or fixture** (a gateway is not the suite's "everything server"): scenarios that call named
  fixtures (`test_simple_text`, `test_input_required_*`, `test_missing_capability`, …) fail with "Unknown tool" because
  exposed names are `server__tool` and the gateway originates no input requests of its own (the
  `input-required-result-*` scenarios; ours is covered by `mcp_consent_mrtr_integration_test.go`); `completion/complete`,
  logging, sampling, subscriptions and SSE-stream scenarios test features the portal doesn't offer;
  `sep-2575-http-server-method-not-found-404-initialize` expects a modern-only server;
  `dns-rebinding-protection/localhost-host-valid-accepted` sends an `Origin` that is not the portal's `APP_URL` (the
  portal accepts only its own origin).

Reproduce: `go build ./cmd/open-mcp-portal ./cmd/fakemcp`, run both (scratch database, `DEV_AUTH=true`), `python3
scripts/dev-seed.py`, create a key, put a header-adding proxy in front, and run the commands above.
