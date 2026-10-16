/*
 * Connect: how to add a gateway to an MCP client. The endpoint URL to copy,
 * the name the client will show (editable; the snippets follow it), and a
 * snippet per client (Claude Code, VS Code, Cursor, curl). Interactive
 * clients sign in with OAuth by default: the snippet holds only the URL, and
 * the client opens the browser the first time it connects. "Use an API key
 * instead" switches to the header variants (for clients that can't open a
 * browser); curl always uses a key. A real key is never put in by itself:
 * only a caller that just created one passes `secret`, when the person
 * asked for it.
 */
import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { terms } from "../../lib/terms";
import { Alert } from "@/components/ui/alert/alert";
import { Card } from "@/components/ui/card/card";
import { CodeBlock } from "@/components/ui/code-block/code-block";
import { CopyField } from "@/components/ui/copy-field/copy-field";
import { Field } from "@/components/ui/field/field";
import { Input } from "@/components/ui/input/input";
import { Switch } from "@/components/ui/switch/switch";
import { Tab, Tabs, TabsList, TabsPanel } from "@/components/ui/tabs/tabs";
import { TextLink } from "@/components/ui/text-link/text-link";
import { authFor, clientName, keyPlaceholder, snippet, snippetClients, type SnippetAuth, type SnippetClient } from "./connect-snippets";
import g from "./gateways.module.css";

const instructions: Record<SnippetAuth, Record<SnippetClient, string>> = {
  oauth: {
    claude: "Run this in a terminal. The first time Claude Code uses the gateway, it opens your browser to sign in.",
    vscode: "Add this to .vscode/mcp.json in your workspace. When VS Code starts the server, it asks you to sign in in your browser.",
    cursor: "Add this to ~/.cursor/mcp.json (or .cursor/mcp.json in a project), then reload Cursor's MCP servers and sign in when it asks.",
    curl: "curl can't sign in with a browser, so it uses an API key. Lists the gateway's tools: a quick check that the URL and the key work.",
  },
  key: {
    claude: "Run this in a terminal. Claude Code adds the gateway to its MCP servers and sends the key on every request.",
    vscode: "Add this to .vscode/mcp.json in your workspace. VS Code asks for the key once and keeps it in its secret storage.",
    cursor: "Add this to ~/.cursor/mcp.json (or .cursor/mcp.json in a project), then reload Cursor's MCP servers.",
    curl: "Lists the gateway's tools: a quick check that the URL and the key work.",
  },
};

type Props = {
  /** The gateway's slug: the default name in the client. */
  slug: string;
  url: string;
  /** A key the person just created and chose to put into the snippets. */
  secret?: string;
  /** Start with the API-key snippets (right after creating a key). */
  preferKey?: boolean;
  /** Inside another card or dialog: no card of its own. */
  bare?: boolean;
  /** The URL is shown just above (a record's Overview). */
  hideUrl?: boolean;
};

export function ConnectSection({ slug, url, secret, preferKey = false, bare = false, hideUrl = false }: Props) {
  const [typed, setTyped] = useState(slug);
  const [client, setClient] = useState<SnippetClient>("claude");
  const [useKey, setUseKey] = useState(preferKey);
  const name = clientName(typed, slug);
  const chosen: SnippetAuth = useKey ? "key" : "oauth";
  const auth = authFor(client, chosen);
  const body = (
    <div className={g.stack}>
      {!hideUrl && <CopyField label="Endpoint URL" name="endpoint URL" value={url} />}
      <Field label="Name in your client" description="How the client lists the gateway. Its tools show up as name__tool in some clients.">
        <Input spellCheck={false} autoComplete="off" maxLength={64} value={typed} onChange={(e) => setTyped(e.target.value)} />
      </Field>
      <Switch
        label="Use an API key instead"
        description="For headless use: clients that can't open a browser, CI and scripts. For automation, use a team service account's key rather than your own."
        checked={useKey}
        onCheckedChange={setUseKey}
      />
      <Tabs value={client} onValueChange={(v) => setClient(v as SnippetClient)}>
        <TabsList aria-label="MCP client">
          {snippetClients.map((c) => (
            <Tab key={c.id} value={c.id}>
              {c.label}
            </Tab>
          ))}
        </TabsList>
        {snippetClients.map((c) => (
          <TabsPanel key={c.id} value={c.id} className={g.snippet}>
            <p className={g.note}>{instructions[authFor(c.id, chosen)][c.id]}</p>
            <CodeBlock code={snippet(c.id, { name, url, secret, auth: chosen })} language={c.language} filename={c.file ?? c.label} wrap />
          </TabsPanel>
        ))}
      </Tabs>
      <ConnectNote auth={auth} secret={Boolean(secret)} />
    </div>
  );
  if (bare) return body;
  return (
    <Card title="Connect" description="Add the gateway to your AI client. It signs you in through your browser, or uses one of your API keys.">
      {body}
    </Card>
  );
}

/** Under the snippets: how sign-in works, where the key comes from, or that the snippets hold a real key. */
function ConnectNote({ auth, secret }: { auth: SnippetAuth; secret: boolean }) {
  if (auth === "oauth") {
    return (
      <p className={g.note}>
        No key needed. On first use the client opens your browser: sign in with your company account and allow it. It stays signed in and refreshes its own
        access; you can disconnect it at any time from <TextLink render={<Link to="/settings/connected-apps" />}>Account › {terms.connectedApps}</TextLink>.
      </p>
    );
  }
  if (secret) return <Alert tone="warning">These snippets contain your API key. Don&apos;t share them or commit them to a repository.</Alert>;
  return (
    <p className={g.note}>
      Replace <code className={g.code}>{keyPlaceholder}</code> with one of your keys from{" "}
      <TextLink render={<Link to="/settings/api-keys" />}>Account › {terms.apiKeys}</TextLink>. For CI, scripts and agents, use a key of a team&apos;s service
      account (Team › {terms.serviceAccounts}) so the automation doesn&apos;t depend on you.
    </p>
  );
}
