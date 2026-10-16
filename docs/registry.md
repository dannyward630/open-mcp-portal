# MCP server registry

Teams register the remote MCP servers they run. The gateway discovers each server's **tools, resources, resource templates and prompts**, keeps track of its health, and publishes it to the company **catalog** under the platform's publishing policy ([decisions](decisions.md) 9 and 10, [SPEC](../SPEC.md) §5 and §7 phase 2). Gateways (phase 3) will build on this catalog.

Code: `internal/registry` (servers, capabilities, publishing, health loop), `internal/upstream` (guarded MCP client, SSRF guard), `internal/secrets` (header value encryption), migration `00003_registry.sql`. Adapted from Grounded's `internal/mcpclient`.

## Servers

| Field | What it is |
|---|---|
| Slug | Unique across the platform and **immutable**: `^[a-z0-9](?:[a-z0-9-]{0,46}[a-z0-9])?$`. Gateways will name tools `slug__tool` |
| Name, description | 1–100 and up to 2000 characters |
| URL | The Streamable HTTP endpoint (see [Security](#security)). Problems return `422 url_not_allowed` with `details.reason` (`invalid`, `too_long`, `userinfo`, `query`, `fragment`, `scheme`, `private_address`) |
| Header and value | Optional static header, such as `Authorization: Bearer …` or `X-API-Key: …`. The value is sealed with `ENCRYPTION_KEY` (AES-256-GCM, bound to the server's ID). It is never returned, logged or audited. Responses show only `hasAuth`, the header name and `authValueHint` (the last 4 characters, and only when the value has at least 8). When you update the server, leaving `authValue` out keeps the stored value, and `authHeaderName: ""` removes both |
| Timeout | 1–120 s (default 30). It bounds the whole exchange |
| Enabled | A disabled server is skipped by the health loop, can't be published and drops out of the catalog |

Creating a server runs discovery straight away. The server is created even if discovery fails; in that case `refreshError` and `lastRefreshError` say why. Changing the URL or the header deletes the server's stored health checks. Changing the URL of a published server under `approval_required` sends it back to `pending` (audited as `mcp_server.publish_request` with reason `url_changed`): it leaves the catalog until a platform admin reviews the new endpoint. Under `self_publish` it stays published. Any URL change also clears `refreshedAt` and reads the capabilities again from the new endpoint at once (a failure is stored as `lastRefreshError`); a platform admin can't approve a server whose capabilities haven't been read from its current URL (`409 not_refreshed`).

## Discovery and change detection

**Refresh** connects and lists every kind the server advertises. It reads every page, accepts at most 500 entries per kind, and gives up after 500 pages or on a repeated cursor. Each response is at most 1 MiB. Titles are clipped to 200 characters and descriptions to 10,000. Names and URIs can be up to 1000 characters; longer or invalid entries are skipped. A definition over 64 KiB fails discovery. A kind that the server advertises but answers with "method not found" counts as empty. Discovery never calls a tool, and the gateway offers no roots, sampling or elicitation.

Each capability stores its **definition**: the listed object as canonical JSON (sorted keys, numbers kept exactly) without `_meta`, which servers use for data that changes between calls. Its **fingerprint** is the SHA-256 of that JSON.

- **approved** means `approved_fingerprint = fingerprint` and the capability is still listed. A gone capability is never approved.
- **changed** means the capability was approved before, its definition is different now, and the server is published. A changed capability drops out of the catalog straight away, without anyone touching it.
- A capability the server stops listing is marked **gone**, and its approval is withdrawn. If the server lists it again later, it needs approval like a new one.
- A refresh never approves anything. On a published server, **new** capabilities stay unapproved too. `pendingChanges` counts every listed capability whose current definition isn't approved: changed ones **and new ones**.

The result reports `listed`, `added` (a returning capability counts as added), `changed` and `gone`, and is audited with those counts.

## Publishing

States are `private`, `pending`, `published` and `rejected`. The **effective policy** is the team's override if one is set, otherwise the platform default (`approval_required` out of the box).

| Action | Who | Effect |
|---|---|---|
| Publish (`note` optional) | team owners and admins | `approval_required`: the server becomes `pending`. `self_publish`: it is `published` straight away and its current capabilities are approved. Errors: `409 not_refreshed`, `disabled`, `already_published`, `already_pending` |
| Review approve | platform admins | `pending` only: the server becomes `published` and its current capabilities are approved |
| Review reject | platform admins | `pending` or `published`: the server becomes `rejected`. Anything else is `409 invalid_state` |
| Unpublish | team owners and admins | `pending`, `published` or `rejected`: the server becomes `private`. A private one gets `409 not_published` |
| Approve a capability | platform admins; team owners and admins only under `self_publish` (otherwise `403 approval_required`) | Published servers only (`409 not_published`). Also `409 not_changed` and `409 capability_gone` |

Publishing actions bump the server's revision. A platform admin sets the default with `PUT /v1/admin/settings/publishing` and a team override with `PUT /v1/admin/teams/{team}/publishing`. Both need `If-Match`; the team override uses the team's revision and bumps it. Changing the policy doesn't change any server's state.

The **catalog** lists servers that are published and enabled, and shows only their approved, listed capabilities. Its counts cover only those capabilities. Its health has a status, a time and an error class, but no message. Any active signed-in person can read it.

## Roles

| | Read servers, capabilities, health and policy | Create, update, test, refresh | Delete, publish, unpublish | Approve changes | Review, settings, overrides |
|---|---|---|---|---|---|
| team member | ✓ | | | | |
| team editor | ✓ | ✓ | | | |
| team owner and admin | ✓ | ✓ | ✓ | under `self_publish` | |
| platform auditor | ✓ (any team, plus admin routes) | | | | |
| platform admin | ✓ (any team, plus admin routes) | | | admin route | ✓ |

Platform roles never change servers through the team routes. People outside the team get `404`, as in phase 1. Archived teams are read-only (`409 team_archived`).

## Health

Test, refresh and the background loop each store a check: ok or failed, latency, trigger (`manual` or `scheduled`), error class, HTTP status, and a short message with the header value and anything secret-looking redacted. The server keeps its latest 100 checks. `statusSince` carries over while the outcome stays the same, so a "failing since" time survives pruning. Error classes: `unavailable`, `auth`, `not_found`, `rate_limited`, `bad_request`, `bad_response`, `config` (the header can't be decrypted), `timeout`, `too_large`, `not_allowed` (blocked address or a redirect).

The loop runs at start-up and then every `HEALTH_CHECK_INTERVAL` (default `5m`, `0` turns it off, minimum `30s`). It tests enabled servers four at a time. Only one replica runs a pass at a time (PostgreSQL advisory lock `omg.mcp-health-checks`), and any server checked within the last 90% of the interval is skipped, so replicas taking turns don't check it twice.

## Security

- **Addresses:** the URL must be https at a public address. Private, loopback, link-local (including cloud metadata), CGNAT, documentation and translation ranges are refused, and so are numeric host notations. The check runs **when the server is saved**, for literal IPs and `localhost`. It runs again **every time the gateway dials**, against every address the host resolves to, and the connection is pinned to the address that was checked. The URL is checked again before every exchange. Redirects are never followed. Proxy environment variables are ignored. URLs can't contain user info, a query or a fragment: credentials belong in the header.
- **Development:** when `DEV_AUTH=true` with a loopback `APP_URL`, plain `http://` to loopback hosts and private or loopback addresses are allowed. No setting allows them in production.
- **Secrets:** `ENCRYPTION_KEY` is required by `serve`. Outside loopback, the server refuses to start with the example key from `.env.example`. `ENCRYPTION_KEY_PREVIOUS` still decrypts values sealed with the old key; re-entering a value re-seals it with the current key. The audit log records only `authValueChanged`.
- **Untrusted text:** descriptions and schemas are prompts a model will read. Review them before you approve, and treat annotations as hints only.

## Local fake server

`make fake-mcp` starts `cmd/fakemcp` on `http://127.0.0.1:8091/mcp`. It has tools `check_outage`, `search_tickets` and `create_ticket`, the resource `status://services`, the template `status://services/{service}` and the prompt `incident_summary`. `-header`/`-value` make it require a header. `-describe tool=text` (or `FAKEMCP_DESCRIBE`, with entries separated by `;`) changes a tool's description, so the next refresh sees a change.

## API

See [`../api/openapi.yaml`](../api/openapi.yaml) (tags `mcp-servers`, `catalog`, `admin`):

- `GET`/`POST /v1/teams/{team}/mcp-servers`, `GET`/`PATCH`/`DELETE …/{serverId}`, `POST …/{serverId}/test`, `…/refresh`, `…/publish`, `…/unpublish`
- `GET …/{serverId}/capabilities?kind=`, `PUT …/{serverId}/capabilities/{capabilityId}/approval`, `GET …/{serverId}/health-checks`
- `GET /v1/teams/{team}/publishing`
- `GET /v1/catalog/servers`, `GET /v1/catalog/servers/{serverId}`
- `GET /v1/admin/mcp-servers?publishState=&changes=&team=`, `GET …/{serverId}`, `GET …/{serverId}/capabilities`, `POST …/{serverId}/review`, `PUT …/{serverId}/capabilities/{capabilityId}/approval`
- `GET`/`PUT /v1/admin/settings/publishing`, `GET`/`PUT /v1/admin/teams/{team}/publishing`

Audit actions: `mcp_server.create`, `.update`, `.delete`, `.refresh`, `.publish_request`, `.publish` (self-publish or approval), `.reject`, `.unpublish`, `mcp_capability.approve`, `settings.publishing_update`, `team.publishing_override`.
