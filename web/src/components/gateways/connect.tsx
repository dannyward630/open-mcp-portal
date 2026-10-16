/*
 * Connect: how to add a gateway to an MCP client. The endpoint URL to copy,
 * the name the client will show (editable; the snippets follow it), and a
 * snippet per client (Claude Code, VS Code, Cursor, curl) with a placeholder
 * for the key. A real key is never put in by itself: only a caller that
 * just created one passes `secret`, when the person asked for it.
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
import { Tab, Tabs, TabsList, TabsPanel } from "@/components/ui/tabs/tabs";
import { TextLink } from "@/components/ui/text-link/text-link";
import { clientName, keyPlaceholder, snippet, snippetClients, type SnippetClient } from "./connect-snippets";
import g from "./gateways.module.css";

const instructions: Record<SnippetClient, string> = {
  claude: "Run this in a terminal. Claude Code adds the gateway to its MCP servers.",
  vscode: "Add this to .vscode/mcp.json in your workspace. VS Code asks for the key once and keeps it in its secret storage.",
  cursor: "Add this to ~/.cursor/mcp.json (or .cursor/mcp.json in a project), then reload Cursor's MCP servers.",
  curl: "Lists the gateway's tools: a quick check that the URL and the key work.",
};

type Props = {
  /** The gateway's slug: the default name in the client. */
  slug: string;
  url: string;
  /** A key the person just created and chose to put into the snippets. */
  secret?: string;
  /** Inside another card or dialog: no card of its own. */
  bare?: boolean;
  /** The URL is shown just above (a record's Overview). */
  hideUrl?: boolean;
};

export function ConnectSection({ slug, url, secret, bare = false, hideUrl = false }: Props) {
  const [typed, setTyped] = useState(slug);
  const [client, setClient] = useState<SnippetClient>("claude");
  const name = clientName(typed, slug);
  const body = (
    <div className={g.stack}>
      {!hideUrl && <CopyField label="Endpoint URL" name="endpoint URL" value={url} />}
      <Field label="Name in your client" description="How the client lists the gateway. Its tools show up as name__tool in some clients.">
        <Input spellCheck={false} autoComplete="off" maxLength={64} value={typed} onChange={(e) => setTyped(e.target.value)} />
      </Field>
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
            <p className={g.note}>{instructions[c.id]}</p>
            <CodeBlock code={snippet(c.id, { name, url, secret })} language={c.language} filename={c.file ?? c.label} wrap />
          </TabsPanel>
        ))}
      </Tabs>
      {secret ? (
        <Alert tone="warning">These snippets contain your API key. Don&apos;t share them or commit them to a repository.</Alert>
      ) : (
        <p className={g.note}>
          Replace <code className={g.code}>{keyPlaceholder}</code> with one of your keys from{" "}
          <TextLink render={<Link to="/settings/api-keys" />}>Account › {terms.apiKeys}</TextLink>. Clients connect with an API key for now; signing in from
          the client comes in a later release.
        </p>
      )}
    </div>
  );
  if (bare) return body;
  return (
    <Card title="Connect" description="Add the gateway to your AI client with its URL and one of your API keys.">
      {body}
    </Card>
  );
}
