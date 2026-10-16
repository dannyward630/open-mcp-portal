/*
 * The fields of an MCP server shared by Add (a form page) and the Settings
 * tab: the address, the timeout, the switch, and the one static header. The
 * header value is replace-only: it's stored encrypted, only its last four
 * characters come back, and an empty field keeps the stored one.
 */
import { ApiError } from "../../api/client";
import { Checkbox } from "@/components/ui/checkbox/checkbox";
import { Field } from "@/components/ui/field/field";
import { Input } from "@/components/ui/input/input";
import { NumberInput } from "@/components/ui/number-input/number-input";
import { Switch } from "@/components/ui/switch/switch";

export type ServerForm = { name: string; description: string; url: string; authHeaderName: string; timeoutSeconds: number; enabled: boolean };

/** The replace-only header value and "remove the header", kept apart from the revisioned form. */
export type AuthEdit = { value: string; remove: boolean };

const headerPattern = /^[A-Za-z0-9!#$%&'*+.^_`|~-]{1,100}$/;

const isLoopback = (host: string) => host === "localhost" || host.endsWith(".localhost") || /^127\.\d+\.\d+\.\d+$/.test(host) || host === "[::1]";

/** What's wrong with a server URL: https only, plain http on a loopback address in development. */
export function urlProblem(url: string, dev: boolean): string | undefined {
  const v = url.trim();
  if (!v) return "Enter the server's URL.";
  let u: URL;
  try {
    u = new URL(v);
  } catch {
    return "Enter a full URL, such as https://tools.example.com/mcp.";
  }
  if (u.protocol === "https:") return undefined;
  if (u.protocol === "http:" && dev && isLoopback(u.hostname)) return undefined;
  return dev ? "Use https. Plain http works only for a loopback address (localhost, 127.0.0.1) in development." : "Use an https address.";
}

export type ServerProblems = Partial<Record<"name" | "url" | "authHeaderName" | "authValue" | "timeoutSeconds", string>>;

/** The form's field errors; `hasAuth` says a header value is stored already (edits). */
export function serverProblems(f: ServerForm, auth: AuthEdit, opts: { dev: boolean; hasAuth: boolean }): ServerProblems {
  const out: ServerProblems = {};
  if (!f.name.trim()) out.name = "Enter a name.";
  else if (f.name.trim().length > 100) out.name = "Use at most 100 characters.";
  const url = urlProblem(f.url, opts.dev);
  if (url) out.url = url;
  if (!Number.isInteger(f.timeoutSeconds) || f.timeoutSeconds < 1 || f.timeoutSeconds > 120) out.timeoutSeconds = "Use a whole number of seconds from 1 to 120.";
  if (auth.remove) return out;
  const name = f.authHeaderName.trim();
  if (name && !headerPattern.test(name)) out.authHeaderName = "Use a header name such as Authorization or X-API-Key (letters, digits and - only).";
  if (!name && auth.value) out.authHeaderName = "Enter the header the value is sent in, such as Authorization.";
  if (name && !auth.value && !opts.hasAuth) out.authValue = "Enter the header's value, or clear the header name.";
  return out;
}

/** Why the server refused a URL (422 url_not_allowed, details.reason), in a sentence. */
const urlReasons: Record<string, string> = {
  invalid: "This isn't a URL the gateway can use. Enter a full address, such as https://tools.example.com/mcp.",
  too_long: "This URL is too long. Use a shorter address.",
  userinfo: "Remove the user name and password from the URL. Send credentials in the header below instead.",
  query: "Remove the query (the part from ?). Send credentials in the header below instead.",
  fragment: "Remove the fragment (the part from #).",
  scheme: "Use an https address.",
  private_address: "The gateway won't connect to a private, loopback or link-local address. Use the server's public address.",
};

/** A server's refusal as field errors: a taken slug, a URL the gateway won't connect to. */
export function serverFieldErrors(err: unknown): { slug?: string; url?: string } {
  if (!(err instanceof ApiError)) return {};
  if (err.code === "slug_taken") return { slug: "Another server uses this slug. Choose another." };
  if (err.code === "url_not_allowed") {
    const reason = typeof err.details?.reason === "string" ? err.details.reason : "";
    return { url: urlReasons[reason] ?? `The gateway won't connect to this address. ${err.message}`.trim() };
  }
  return {};
}

type FieldsProps = {
  form: ServerForm;
  set: (patch: Partial<ServerForm>) => void;
  errors: ServerProblems;
  disabled?: boolean;
  dev: boolean;
};

export function UrlField({ form, set, errors, disabled, dev }: FieldsProps) {
  return (
    <Field
      label="URL"
      description={
        dev
          ? "The server's Streamable HTTP endpoint, such as https://tools.example.com/mcp. In development, plain http also works on a loopback address (http://localhost:9000/mcp)."
          : "The server's Streamable HTTP endpoint, such as https://tools.example.com/mcp. https only."
      }
      error={errors.url}
    >
      <Input type="url" inputMode="url" autoComplete="off" spellCheck={false} disabled={disabled} value={form.url} onChange={(e) => set({ url: e.target.value })} />
    </Field>
  );
}

export function TimeoutField({ form, set, errors, disabled }: FieldsProps) {
  return (
    <Field label="Timeout (seconds)" description="How long one request to the server may take, 1 to 120." error={errors.timeoutSeconds}>
      <NumberInput maximumFractionDigits={0} min={1} max={120} disabled={disabled} value={String(form.timeoutSeconds)} onValueChange={(v) => set({ timeoutSeconds: Number(v) })} />
    </Field>
  );
}

export function EnabledSwitch({ form, set, disabled }: FieldsProps) {
  return <Switch label="Enabled" description="Turned off, nothing connects to the server, and it can't be published." disabled={disabled} checked={form.enabled} onCheckedChange={(v) => set({ enabled: v })} />;
}

type AuthProps = FieldsProps & { auth: AuthEdit; setAuth: (a: AuthEdit) => void; hasAuth: boolean; hint?: string };

/** The header name, the replace-only value (masked) and, when one is stored, "Remove the header". */
export function AuthFields({ form, set, errors, disabled, auth, setAuth, hasAuth, hint }: AuthProps) {
  const off = disabled || auth.remove;
  return (
    <>
      <Field label="Header name" labelHint="Optional" description="Sent with every request, such as Authorization or X-API-Key. Leave empty if the server needs none." error={errors.authHeaderName}>
        <Input autoComplete="off" spellCheck={false} disabled={off} value={form.authHeaderName} onChange={(e) => set({ authHeaderName: e.target.value })} />
      </Field>
      <Field
        label={hasAuth ? "Replace header value" : "Header value"}
        description={
          hasAuth
            ? `The stored value ends in ${hint || "…"}. Leave empty to keep it. It's never shown again.`
            : "Stored encrypted and never shown again, such as Bearer and a token."
        }
        error={errors.authValue}
      >
        <Input type="password" autoComplete="new-password" disabled={off} value={auth.value} onChange={(e) => setAuth({ ...auth, value: e.target.value })} />
      </Field>
      {hasAuth && <Checkbox label="Remove the header and its stored value" disabled={disabled} checked={auth.remove} onCheckedChange={(v) => setAuth({ value: "", remove: v })} />}
    </>
  );
}
