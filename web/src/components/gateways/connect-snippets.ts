/*
 * The Connect section's snippets for each MCP client. They hold the
 * gateway's URL and a placeholder for the key; a real key goes in only when
 * the caller passes one (right after creating a key, if the person asks).
 */

/** What the snippets show instead of a key. */
export const keyPlaceholder = "<YOUR_API_KEY>";

/** The VS Code input that prompts for the key (and stores it in VS Code's secret storage). */
export const vscodeInputId = "omcp-key";

export type SnippetClient = "claude" | "vscode" | "cursor" | "curl";

export const snippetClients: { id: SnippetClient; label: string; language: string; file?: string }[] = [
  { id: "claude", label: "Claude Code", language: "bash" },
  { id: "vscode", label: "VS Code", language: "json", file: ".vscode/mcp.json" },
  { id: "cursor", label: "Cursor", language: "json", file: "~/.cursor/mcp.json" },
  { id: "curl", label: "curl", language: "bash" },
];

/** A name clients accept: letters, digits, hyphens and underscores ("Support tools!" → "Support-tools"). */
export function clientName(name: string, fallback: string) {
  const clean = name
    .trim()
    .replace(/[^A-Za-z0-9_-]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 64);
  return clean || fallback;
}

type Input = { name: string; url: string; secret?: string };

/** One client's snippet. */
export function snippet(client: SnippetClient, { name, url, secret }: Input): string {
  const key = secret ?? keyPlaceholder;
  switch (client) {
    case "claude":
      return `claude mcp add --transport http ${name} ${url} --header "Authorization: Bearer ${key}"`;
    case "vscode":
      // VS Code asks for the key once and keeps it in its secret storage: never written into the file.
      return JSON.stringify(
        {
          inputs: [{ type: "promptString", id: vscodeInputId, description: "Open MCP Gateway API key", password: true }],
          servers: { [name]: { type: "http", url, headers: { Authorization: `Bearer \${input:${vscodeInputId}}` } } },
        },
        null,
        2,
      );
    case "cursor":
      return JSON.stringify({ mcpServers: { [name]: { url, headers: { Authorization: `Bearer ${key}` } } } }, null, 2);
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
