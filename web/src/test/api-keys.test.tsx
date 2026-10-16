/* Account › API keys: the list, Create (the secret once, never again), the choice to fill snippets, and Revoke. */
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { axe } from "vitest-axe";
import type { Schemas } from "../api/client";
import { apiKey, myGateway } from "./fixtures";
import { type Handler, Reply, mockApi, renderApp, shellRoutes } from "./harness";

const secret = "omcp_ab12cd34_0123456789abcdefghijklmnopqrstuv";
const created: Schemas["ApiKeyCreated"] = { key: apiKey({ id: "new-key", name: "CI runner", lastUsedAt: null }), secret };

const routes = (extra: Record<string, Handler> = {}): Record<string, Handler> => ({
  ...shellRoutes("none", "member"),
  "GET /v1/me/api-keys": () => [apiKey(), apiKey({ id: "old", name: "Old laptop", status: "revoked", revokedAt: "2026-10-10T10:00:00Z", lastUsedAt: null })],
  "GET /v1/me/gateways": () => [myGateway()],
  ...extra,
});

describe("API keys", () => {
  it("lists the person's keys with status, last use and expiry, from the account menu", async () => {
    mockApi(routes());
    const { container } = renderApp("/settings/api-keys");
    expect(await screen.findByRole("heading", { level: 1, name: "API keys" })).toBeInTheDocument();
    const table = await screen.findByRole("table", { name: "API keys" });
    const active = (await within(table).findByText("Claude Code on my laptop")).closest("tr")!;
    expect(active).toHaveTextContent("omcp_ab12cd34…");
    expect(active).toHaveTextContent("Active");
    const revoked = within(table).getByText("Old laptop").closest("tr")!;
    expect(revoked).toHaveTextContent("Revoked");
    expect(revoked).toHaveTextContent("Never");
    // Revoked keys can't be revoked again: no row menu.
    expect(within(revoked).queryByRole("button", { name: /Actions for/ })).toBeNull();
    await waitFor(() => expect(document.title).toBe("API keys · Open MCP Gateway"));
    expect(await axe(container)).toHaveNoViolations();
  });

  it("shows the secret once after creating a key, and never again", async () => {
    const u = userEvent.setup();
    const calls = mockApi(routes({ "POST /v1/me/api-keys": () => new Reply(201, { data: created }) }));
    const { container } = renderApp("/settings/api-keys");
    await u.click(await screen.findByRole("button", { name: "Create API key" }));
    const dialog = await screen.findByRole("dialog", { name: "Create API key" });
    expect(within(dialog).getByRole("textbox", { name: /Expires after/ })).toHaveValue("90");
    await u.click(within(dialog).getByRole("button", { name: "Create key" }));
    expect(await within(dialog).findByText("Enter a name.")).toBeInTheDocument();
    await u.type(within(dialog).getByRole("textbox", { name: "Name" }), "CI runner");
    await u.click(within(dialog).getByRole("button", { name: "Create key" }));
    await waitFor(() => expect(calls.find((c) => c.method === "POST")?.body).toEqual({ name: "CI runner", expiresInDays: 90 }));

    const shown = await screen.findByRole("dialog", { name: "API key created" });
    expect(within(shown).getByText("Copy this key now.")).toBeInTheDocument();
    expect(within(shown).getByText(/It won't be shown again/)).toBeInTheDocument();
    expect(within(shown).getByText(secret)).toBeInTheDocument();
    expect(within(shown).getByRole("button", { name: /Copy API key secret/ })).toBeInTheDocument();
    // The snippets keep the placeholder unless the person asks for the key in them.
    expect(within(shown).getByText(/claude mcp add/).textContent).toContain("<YOUR_API_KEY>");
    expect(await axe(container)).toHaveNoViolations();
    await u.click(within(shown).getByRole("checkbox", { name: "Put this key into the snippets" }));
    expect(within(shown).getByText(/claude mcp add/).textContent).toContain(`Bearer ${secret}`);
    expect(within(shown).getByText(/These snippets contain your API key/)).toBeInTheDocument();

    await u.click(within(shown).getByRole("button", { name: "Done" }));
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "API key created" })).toBeNull());
    expect(document.body.textContent).not.toContain(secret);
    // Opening Create again starts over: the old secret is gone.
    await u.click(screen.getByRole("button", { name: "Create API key" }));
    expect(await screen.findByRole("dialog", { name: "Create API key" })).toBeInTheDocument();
    expect(document.body.textContent).not.toContain(secret);
  });

  it("checks the expiry and shows a refusal", async () => {
    const u = userEvent.setup();
    mockApi(routes({ "POST /v1/me/api-keys": () => Reply.error(409, "too_many_keys", "You have 20 active keys. Revoke one first.") }));
    renderApp("/settings/api-keys");
    await u.click(await screen.findByRole("button", { name: "Create API key" }));
    const dialog = await screen.findByRole("dialog", { name: "Create API key" });
    await u.type(within(dialog).getByRole("textbox", { name: "Name" }), "One more");
    const days = within(dialog).getByRole("textbox", { name: /Expires after/ });
    await u.clear(days);
    await u.type(days, "400");
    await u.click(within(dialog).getByRole("button", { name: "Create key" }));
    expect(await within(dialog).findByText("Enter a whole number of days from 1 to 365.")).toBeInTheDocument();
    await u.clear(days);
    await u.type(days, "30");
    await u.click(within(dialog).getByRole("button", { name: "Create key" }));
    expect(await within(dialog).findByText("You have 20 active keys. Revoke one first.")).toBeInTheDocument();
  });

  it("revokes a key after confirming", async () => {
    const u = userEvent.setup();
    const key = apiKey();
    const calls = mockApi(routes({ [`DELETE /v1/me/api-keys/${key.id}`]: () => ({ ok: true }) }));
    renderApp("/settings/api-keys");
    await u.click(await screen.findByRole("button", { name: `Actions for ${key.name}` }));
    await u.click(await screen.findByRole("menuitem", { name: "Revoke…" }));
    const ask = await screen.findByRole("alertdialog", { name: `Revoke ${key.name}?` });
    expect(calls.some((c) => c.method === "DELETE")).toBe(false);
    await u.click(within(ask).getByRole("button", { name: "Revoke key" }));
    await waitFor(() => expect(calls.some((c) => c.method === "DELETE" && c.url === `/v1/me/api-keys/${key.id}`)).toBe(true));
  });

  it("is in the account menu next to Profile", async () => {
    const u = userEvent.setup();
    mockApi(routes());
    renderApp("/");
    await u.click(await screen.findByRole("button", { name: /Una User/ }));
    const items = (await screen.findAllByRole("menuitem")).map((i) => i.textContent);
    expect(items.indexOf("API keys")).toBe(items.indexOf("Profile") + 1);
  });
});
