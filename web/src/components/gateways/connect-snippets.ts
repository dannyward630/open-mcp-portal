/*
 * The Connect section's snippets for each MCP client. By default the
 * interactive clients (Claude Code, VS Code, Cursor) connect with OAuth
 * sign-in: the snippet is just the gateway's URL, with no header and no
 * secret, and the client opens the browser on first use. The API-key
 * variants (for headless use) hold a placeholder for the key; a real key
 * goes in only when the caller passes one (right after creating a key, if
 * the person asks). curl has no browser, so it always uses a key.
 */

/** What the snippets show instead of a key. */
export const keyPlaceholder = "<YOUR_API_KEY>";

/** The VS Code input that prompts for the key (and stores it in VS Code's secret storage). */
export const vscodeInputId = "omp-key";

export type SnippetClient = "claude" | "vscode" | "cursor" | "curl";

/** How the client proves who it is: OAuth sign-in in the browser, or an API key in a header. */
export type SnippetAuth = "oauth" | "key";

export const snippetClients: { id: SnippetClient; label: string; language: string; file?: string; oauth: boolean }[] = [
  { id: "claude", label: "Claude Code", language: "bash", oauth: true },
  { id: "vscode", label: "VS Code", language: "json", file: ".vscode/mcp.json", oauth: true },
  { id: "cursor", label: "Cursor", language: "json", file: "~/.cursor/mcp.json", oauth: true },
  { id: "curl", label: "curl", language: "bash", oauth: false },
];

/** The way a client connects: OAuth unless the person chose a key, or the client can't sign in (curl). */
export function authFor(client: SnippetClient, auth: SnippetAuth): SnippetAuth {
  return snippetClients.find((c) => c.id === client)?.oauth ? auth : "key";
}

/** A name clients accept: letters, digits, hyphens and underscores ("Support tools!" → "Support-tools"). */
export function clientName(name: string, fallback: string) {
  const clean = name
    .trim()
    .replace(/[^A-Za-z0-9_-]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 64);
  return clean || fallback;
}

type Input = { name: string; url: string; secret?: string; auth?: SnippetAuth };

const json = (v: unknown) => JSON.stringify(v, null, 2);

/** One client's snippet (OAuth by default; `auth: "key"` for the API-key variant). */
export function snippet(client: SnippetClient, { name, url, secret, auth = "oauth" }: Input): string {
  const key = secret ?? keyPlaceholder;
  const withKey = authFor(client, auth) === "key";
  switch (client) {
    case "claude":
      return withKey ? `claude mcp add --transport http ${name} ${url} --header "Authorization: Bearer ${key}"` : `claude mcp add --transport http ${name} ${url}`;
    case "vscode":
      if (!withKey) return json({ servers: { [name]: { type: "http", url } } });
      // VS Code asks for the key once and keeps it in its secret storage: never written into the file.
      return json({
        inputs: [{ type: "promptString", id: vscodeInputId, description: "Open MCP Portal API key", password: true }],
        servers: { [name]: { type: "http", url, headers: { Authorization: `Bearer \${input:${vscodeInputId}}` } } },
      });
    case "cursor":
      return withKey ? json({ mcpServers: { [name]: { url, headers: { Authorization: `Bearer ${key}` } } } }) : json({ mcpServers: { [name]: { url } } });
    case "curl":
      return [
        `curl -X POST ${url} \\`,
        `  -H "Authorization: Bearer ${key}" \\`,
        `  -H "Content-Type: application/json" \\`,
        `  -H "Accept: application/json, text/event-stream" \\`,
        `  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'`,
      ].join("\n");
  }
}
