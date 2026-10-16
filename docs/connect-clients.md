# Connecting MCP clients to a gateway

Every gateway has one MCP endpoint, `<gateway URL>/mcp/<gateway>`, for example `https://mcp.example.com/mcp/support`. You find it, with ready-made snippets, under **My gateways** (or the gateway's **Overview › Connect**). There are three ways to connect:

| Who connects | Use | Credential |
|---|---|---|
| You, in an interactive client (Claude Code, Claude, VS Code, Cursor) | **OAuth sign-in** (the default) | None to copy: you sign in in the browser, the client keeps short-lived tokens |
| You, from a script or a client that can't sign in | A **personal API key** (`omcp_…`) | Account › API keys |
| A pipeline, agent or job that must not depend on one person | A **service account key** (`omcp_sa_…`) | Team › Service accounts |

All three see only what the caller may use on that gateway, checked again on every request. The gateway never passes your credential on to MCP servers; it sends them a signed identity instead ([upstream-identity.md](upstream-identity.md)).

## OAuth sign-in (Claude Code, VS Code, Cursor, Claude)

Add the gateway's URL **without any header**. The first time the client talks to the gateway, it gets `401` with a pointer to the gateway's sign-in metadata, opens your browser, and:

1. You sign in with your company account, if you aren't signed in already.
2. The consent page names the app, the host that vouches for it, the gateway it wants, and where your browser goes back to. Choose **Allow** (or **Deny**).
3. The browser returns to the app, which now has tokens and refreshes them by itself.

Consent is remembered **per app and gateway**: connecting the same app to another gateway asks again (decision 21). Apps your platform admins trust (Claude, Claude Code and VS Code out of the box) skip the consent page, but you still sign in.

**Claude Code**

```sh
claude mcp add --transport http support https://mcp.example.com/mcp/support
```

Then run `/mcp` in Claude Code and choose the server to sign in.

**VS Code** (`.vscode/mcp.json`, or your user configuration)

```json
{
  "servers": {
    "support": { "type": "http", "url": "https://mcp.example.com/mcp/support" }
  }
}
```

VS Code asks to sign in when it starts the server.

**Cursor** (`~/.cursor/mcp.json`)

```json
{
  "mcpServers": {
    "support": { "url": "https://mcp.example.com/mcp/support" }
  }
}
```

Cursor shows a sign-in prompt for the server in its MCP settings.

**Claude (claude.ai and Claude Desktop):** add a custom connector with the gateway's URL. The gateway must be reachable from the internet for claude.ai.

### What a connected app can do

- It acts as **you**, on **that gateway only**: its token is refused by every other gateway and by the management API.
- Every request checks your account (suspended people are refused), your current access to the gateway (removing you from the team or the grant stops it at the next request) and that the connection still exists.
- Access tokens last an hour; the app refreshes them for up to 30 days of inactivity. If a refresh token is ever used twice (a sign of theft), the connection is revoked and you sign in again.
- **Account › Connected apps** lists your connections per gateway, with their last use, and **Disconnect** stops one at once and forgets the consent. Platform admins can disconnect anyone's apps from the person's page.

### Apps without a metadata document

Clients identify themselves either with a *Client ID Metadata Document* (an https URL their maker publishes; the consent page shows that host) or, for older clients, by registering themselves (Dynamic Client Registration). Self-registered apps are marked **registered** on the consent page: anyone can register any name, so check the redirect host before you allow it. They are never on the trusted list, and a registration unused for 30 days expires (the app registers again).

If the browser shows **"Can't connect this app"**, the app's request was refused before anything was shared, for example an unknown app, a redirect address the app never registered, or a metadata document the gateway can't fetch. Nothing is sent to the app in that case. Errors after that (no PKCE, a URL that isn't one of your gateways, a gateway you can't use) go back to the app, which usually shows them.

## Personal API keys

For scripts, curl, and clients that can't open a browser. Create one under **Account › API keys** (it is shown once), optionally limited to some gateways, with an expiry of up to 365 days. Send it as a bearer token:

```sh
claude mcp add --transport http support https://mcp.example.com/mcp/support \
  --header "Authorization: Bearer omcp_..."
```

```json
{
  "inputs": [{ "type": "promptString", "id": "omcp-key", "description": "Open MCP Gateway API key", "password": true }],
  "servers": {
    "support": { "type": "http", "url": "https://mcp.example.com/mcp/support",
                 "headers": { "Authorization": "Bearer ${input:omcp-key}" } }
  }
}
```

```sh
curl -X POST https://mcp.example.com/mcp/support \
  -H "Authorization: Bearer omcp_..." -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'
```

A personal key stops working when you leave the team or are suspended, like everything that acts as you.

## Service accounts

For CI, agents and jobs owned by a **team** rather than a person. Team owners and admins manage them under **Team › Service accounts**:

- An account has a name (unique in the team), a description, a **role** of `member` or `editor` (used for tools that require a minimum team role), and can be turned off.
- It can use the **team's own gateways**, and **platform gateways granted to the team**. Grants to people or SSO groups don't cover it.
- Its keys (`omcp_sa_…`) are shown once, always expire (at most 365 days), and can be limited to some gateways. An account has at most **two active keys**, so you can rotate: create the new key, deploy it, then revoke the old one.
- Turning the account off, deleting it, archiving the team, or revoking or expiring a key refuses the next request. The account doesn't depend on the person who created it.

Use its key exactly like a personal key (`Authorization: Bearer omcp_sa_...`). MCP servers see `principal_type: service_account` with the account's ID and name and its team, and no email. Calls are recorded under the account's name; owners and admins can filter the team's call log by service account.

## Troubleshooting

| Symptom | Likely cause |
|---|---|
| `401` with `error="invalid_token"` | The key or token was revoked or expired, the person is suspended, the service account is off or its team archived, or an OAuth token was used on another gateway. Sign in again or use a valid key |
| `404 Gateway not found` | The gateway doesn't exist or is off, you (or the account) can't use it, or the key is limited to other gateways |
| A tool you expect is missing | It isn't selected in this gateway, its server is unavailable, or your (or the account's) team role is below the tool's minimum |
| The browser keeps asking for consent | The app isn't trusted and you disconnected it, or it connects to a different gateway (one consent per gateway) |
