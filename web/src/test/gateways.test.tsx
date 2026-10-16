/* A team's gateways: the list, Create, the record's tabs (Connect, capabilities and the picker, activity, settings) and role-gated actions. */
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { axe } from "vitest-axe";
import type { Schemas } from "../api/client";
import { gateway, gatewayCapabilities, gatewayCapability, gatewayId, gatewayOptions, mcpCall, page, team } from "./fixtures";
import { type Handler, Reply, mockApi, renderApp, shellRoutes } from "./harness";

type Role = Schemas["TeamRole"];
const base = `/v1/teams/${team.slug}/gateways`;
const list = `/teams/${team.slug}/gateways`;
const record = `${list}/${gatewayId}`;

const routes = (role: Role | null = "owner", g = gateway(), extra: Record<string, Handler> = {}, platformRole: Schemas["PlatformRole"] = "none"): Record<string, Handler> => ({
  ...shellRoutes(platformRole, role),
  [`GET ${base}`]: () => page([g]),
  [`GET ${base}/${gatewayId}`]: () => g,
  [`GET ${base}/${gatewayId}/capabilities`]: () => gatewayCapabilities(),
  [`GET /v1/teams/${team.slug}/gateway-options`]: () => gatewayOptions,
  [`GET /v1/teams/${team.slug}/calls`]: () => page([mcpCall()]),
  ...extra,
});

describe("Team › Gateways list", () => {
  it("shows members each gateway's switch, capabilities and unavailable ones, read-only", async () => {
    mockApi(routes("member", gateway({ unavailableCount: 1 })));
    const { container } = renderApp(list);
    expect(await screen.findByRole("heading", { level: 1, name: "Gateways" })).toBeInTheDocument();
    const table = await screen.findByRole("table", { name: "Gateways" });
    const row = (await within(table).findByText("Support tools")).closest("tr")!;
    expect(row).toHaveTextContent("support-tools");
    expect(row).toHaveTextContent("Enabled");
    expect(row).toHaveTextContent("2 capabilities");
    expect(row).toHaveTextContent("1 unavailable");
    expect(within(row).getByRole("link", { name: "Support tools" })).toHaveAttribute("href", record);
    expect(screen.queryByRole("button", { name: "Create gateway" })).toBeNull();
    expect(screen.getByText("Members can view the team's gateways. Admins and owners can create and change them.")).toBeInTheDocument();
    expect(await axe(container)).toHaveNoViolations();
  });

  it("explains what a gateway is when there are none, and offers Create to admins but not editors", async () => {
    mockApi(routes("admin", gateway(), { [`GET ${base}`]: () => page([]) }));
    const { unmount } = renderApp(list);
    expect(await screen.findByText("No gateways yet.")).toBeInTheDocument();
    expect(screen.getByText(/curated set of tools and prompts at one MCP address/)).toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: "Create gateway" }).length).toBeGreaterThan(0);
    unmount();

    mockApi(routes("editor", gateway(), { [`GET ${base}`]: () => page([]) }));
    renderApp(list);
    expect(await screen.findByText("No gateways yet.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Create gateway" })).toBeNull();
  });

  it("is open to platform staff who aren't members", async () => {
    mockApi(routes(null, gateway(), {}, "platform_auditor"));
    renderApp(list);
    expect(await within(await screen.findByRole("table", { name: "Gateways" })).findByText("Support tools")).toBeInTheDocument();
    expect(screen.queryByText("Only team members can see this.")).toBeNull();
  });
});

describe("Create gateway", () => {
  it("suggests the slug from the name, shows a taken slug on its field, then opens the new gateway's capabilities", async () => {
    const u = userEvent.setup();
    let taken = true;
    const calls = mockApi(routes("owner", gateway(), { [`POST ${base}`]: () => (taken ? Reply.error(409, "slug_taken", "Slug taken") : new Reply(201, { data: gateway() })) }));
    const { router } = renderApp(`${list}?form=new`);
    await u.type(await screen.findByRole("textbox", { name: "Name" }), "Support Tools!");
    expect(screen.getByRole("textbox", { name: "Slug" })).toHaveValue("support-tools");
    expect(screen.getByText(/Can't be changed later: the endpoint is \/mcp\/slug/)).toBeInTheDocument();
    await u.click(screen.getByRole("button", { name: "Create gateway" }));
    expect(await screen.findByText("Another gateway uses this slug. Choose another.")).toBeInTheDocument();
    taken = false;
    // The refusal belongs to the slug that was sent: changing it clears it, so the form can be sent again.
    await u.type(screen.getByRole("textbox", { name: "Slug" }), "-2");
    await u.click(screen.getByRole("button", { name: "Create gateway" }));
    await waitFor(() => expect(calls.filter((c) => c.method === "POST").at(-1)?.body).toEqual({ slug: "support-tools-2", name: "Support Tools!", enabled: true }));
    expect(await screen.findByRole("heading", { level: 1, name: "Support tools" })).toBeInTheDocument();
    await waitFor(() => expect(router.state.location.search).toMatchObject({ tab: "capabilities" }));
  });

  it("needs a name", async () => {
    const u = userEvent.setup();
    const calls = mockApi(routes("owner"));
    renderApp(`${list}?form=new`);
    await u.click(await screen.findByRole("button", { name: "Create gateway" }));
    expect(await screen.findByText("Enter a name.")).toBeInTheDocument();
    expect(calls.some((c) => c.method === "POST")).toBe(false);
  });
});

describe("Gateway › Overview and Connect", () => {
  it("shows the endpoint to copy, the switch, unavailable capabilities and snippets without a secret", async () => {
    const u = userEvent.setup();
    const off = gatewayCapability({ capabilityId: "ffffffff-ffff-4fff-8fff-ffffffffffff", name: "restart", exposedName: "service-status__restart", available: false, unavailableReason: "server_disabled" });
    mockApi(routes("member", gateway({ unavailableCount: 1 }), { [`GET ${base}/${gatewayId}/capabilities`]: () => gatewayCapabilities([gatewayCapability(), off]) }));
    const { container } = renderApp(record);
    expect(await screen.findByRole("heading", { level: 1, name: "Support tools" })).toBeInTheDocument();
    expect(screen.getByRole("tablist", { name: "Gateway sections" })).toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: /Access/ })).toBeNull();
    expect(screen.getByRole("button", { name: /Copy endpoint URL/ })).toBeInTheDocument();
    expect(screen.getAllByText("https://mcp.example.com/mcp/support-tools").length).toBeGreaterThan(0);
    expect(await screen.findByText("1 capability isn't available right now")).toBeInTheDocument();
    expect(within(screen.getByRole("list", { name: "Unavailable capabilities" })).getByText(/Its server is turned off/)).toBeInTheDocument();

    const claude = screen.getByText(/claude mcp add --transport http/);
    expect(claude.textContent).toBe('claude mcp add --transport http support-tools https://mcp.example.com/mcp/support-tools --header "Authorization: Bearer <YOUR_API_KEY>"');
    expect(screen.getByRole("link", { name: "Account › API keys" })).toHaveAttribute("href", "/settings/api-keys");
    await waitFor(() => expect(document.title).toBe("Support tools · Gateways · Open MCP Gateway"));
    expect(await axe(container)).toHaveNoViolations();

    // The name follows the field; VS Code prompts for the key instead of holding it.
    const name = screen.getByRole("textbox", { name: "Name in your client" });
    await u.clear(name);
    await u.type(name, "support desk");
    expect(screen.getByText(/claude mcp add/).textContent).toContain("http support-desk https://");
    await u.click(screen.getByRole("tab", { name: "VS Code" }));
    const vscode = await screen.findByText(/"promptString"/);
    expect(vscode.textContent).toContain('"password": true');
    expect(vscode.textContent).toContain('"Authorization": "Bearer ${input:omcp-key}"');
    expect(vscode.textContent).toContain('"url": "https://mcp.example.com/mcp/support-tools"');
    await u.click(screen.getByRole("tab", { name: "Cursor" }));
    expect((await screen.findByText(/"mcpServers"/)).textContent).toContain('"Authorization": "Bearer <YOUR_API_KEY>"');
    expect(document.body.textContent).not.toMatch(/omcp_[A-Za-z0-9]{12,}/);
  });

  it("shows the not-found page for an unknown gateway", async () => {
    mockApi(routes("owner", gateway(), { [`GET ${base}/${gatewayId}`]: () => Reply.error(404, "not_found") }));
    renderApp(record);
    expect(await screen.findByRole("heading", { level: 1, name: "Gateway not found" })).toBeInTheDocument();
  });
});

describe("Gateway › Activity", () => {
  it("lists calls through the gateway for owners, filtered by outcome on the server", async () => {
    const calls = mockApi(routes("owner"));
    const { container } = renderApp(`${record}?tab=activity&outcome=denied`);
    const table = await screen.findByRole("table", { name: "Calls" });
    expect(await within(table).findByText("service-status__get_status")).toBeInTheDocument();
    expect(within(table).getByText("API key: Claude Code on my laptop")).toBeInTheDocument();
    expect(screen.getByText(/Arguments and results are never stored/)).toBeInTheDocument();
    await waitFor(() => expect(calls.some((c) => c.url.endsWith("/calls") && c.search.get("gateway") === gatewayId && c.search.get("outcome") === "denied")).toBe(true));
    expect(await axe(container)).toHaveNoViolations();
  });

  it("tells members who can see the calls, without asking for them", async () => {
    const calls = mockApi(routes("member"));
    renderApp(`${record}?tab=activity`);
    expect(await screen.findByText("Only the team's owners and admins see this gateway's calls.")).toBeInTheDocument();
    expect(calls.some((c) => c.url.endsWith("/calls"))).toBe(false);
  });
});

describe("Gateway › Settings", () => {
  it("saves with If-Match, and owners delete after confirming", async () => {
    const u = userEvent.setup();
    let current = gateway();
    const calls = mockApi(
      routes("owner", current, {
        [`GET ${base}/${gatewayId}`]: () => current,
        [`PATCH ${base}/${gatewayId}`]: () => (current = gateway({ name: "Support", revision: 8 })),
        [`DELETE ${base}/${gatewayId}`]: () => ({ ok: true }),
      }),
    );
    const { container } = renderApp(`${record}?tab=settings`);
    const name = await screen.findByRole("textbox", { name: "Name" });
    expect(await axe(container)).toHaveNoViolations();
    await u.clear(name);
    await u.type(name, "Support");
    await u.click(screen.getByRole("button", { name: "Save settings" }));
    await waitFor(() => expect(calls.find((c) => c.method === "PATCH")?.body).toEqual({ name: "Support", description: gateway().description, enabled: true }));
    expect(calls.find((c) => c.method === "PATCH")?.headers.get("If-Match")).toBe('"7"');

    await u.click(screen.getByRole("button", { name: "Delete gateway" }));
    await u.click(within(await screen.findByRole("alertdialog", { name: /Delete Support/ })).getByRole("button", { name: "Delete gateway" }));
    await waitFor(() => expect(calls.some((c) => c.method === "DELETE" && c.url === `${base}/${gatewayId}`)).toBe(true));
    expect(await screen.findByRole("heading", { level: 1, name: "Gateways" })).toBeInTheDocument();
  });

  it("is read-only for editors, who can't delete", async () => {
    mockApi(routes("editor"));
    renderApp(`${record}?tab=settings`);
    expect(await screen.findByText("Members can view the gateway's settings. Owners and admins can change them.")).toBeInTheDocument();
    expect(screen.getByRole("textbox", { name: "Name" })).toBeDisabled();
    expect(screen.queryByRole("button", { name: "Delete gateway" })).toBeNull();
  });
});

describe("the command palette", () => {
  it("offers owners Create gateway, which opens the form", async () => {
    const u = userEvent.setup();
    mockApi(routes("owner"));
    const { router } = renderApp(`/teams/${team.slug}`);
    await screen.findByRole("heading", { level: 1, name: team.name });
    await u.keyboard("{Control>}k{/Control}");
    const dialog = await screen.findByRole("dialog", { name: "Command palette" });
    await u.type(within(dialog).getByRole("combobox", { name: "Command palette" }), "create gateway");
    await u.keyboard("{Enter}");
    await waitFor(() => expect(router.state.location.pathname).toBe(list));
    expect(await screen.findByRole("heading", { level: 1, name: "Create gateway" })).toBeInTheDocument();
  });

  it("gives members the pages but not Create gateway", async () => {
    const u = userEvent.setup();
    mockApi(routes("member"));
    renderApp(`/teams/${team.slug}`);
    await screen.findByRole("heading", { level: 1, name: team.name });
    await u.keyboard("{Control>}k{/Control}");
    const dialog = await screen.findByRole("dialog", { name: "Command palette" });
    await u.type(within(dialog).getByRole("combobox", { name: "Command palette" }), "gateway");
    expect(within(dialog).getByRole("option", { name: /^Gateways/ })).toBeInTheDocument();
    expect(within(dialog).getByRole("option", { name: /My gateways/ })).toBeInTheDocument();
    expect(within(dialog).queryByRole("option", { name: /Create gateway/ })).toBeNull();
  });
});
