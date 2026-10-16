/* Pure gateway helpers: the clients' snippets, the refused-selection mapping (422) and the audit words. */
import { ApiError } from "../api/client";
import { actionLabel, areaOf, areaOptions, targetTypeLabel } from "../components/audit/labels";
import { clientName, keyPlaceholder, snippet, snippetClients } from "../components/gateways/connect-snippets";
import { selectionProblem } from "../components/gateways/picker-errors";
import { filterOptions } from "../components/gateways/picker-options";
import { gatewayOptions } from "./fixtures";

const url = "https://mcp.example.com/mcp/support-tools";

describe("connect snippets", () => {
  it("hold the URL and a placeholder, never a key unless one is passed", () => {
    for (const c of snippetClients) {
      const text = snippet(c.id, { name: "support-tools", url });
      expect(text, c.id).toContain(url);
      expect(text, c.id).not.toContain("omcp_");
    }
    expect(snippet("claude", { name: "support-tools", url })).toBe(`claude mcp add --transport http support-tools ${url} --header "Authorization: Bearer ${keyPlaceholder}"`);
    expect(snippet("cursor", { name: "s", url, secret: "omcp_secret" })).toContain('"Authorization": "Bearer omcp_secret"');
    expect(snippet("curl", { name: "s", url, secret: "omcp_secret" })).toContain('-H "Authorization: Bearer omcp_secret"');
  });

  it("give VS Code a password prompt instead of the key, even when one is passed", () => {
    const parsed = JSON.parse(snippet("vscode", { name: "support-tools", url, secret: "omcp_secret" }));
    expect(parsed).toEqual({
      inputs: [{ type: "promptString", id: "omcp-key", description: "Open MCP Gateway API key", password: true }],
      servers: { "support-tools": { type: "http", url, headers: { Authorization: "Bearer ${input:omcp-key}" } } },
    });
    expect(JSON.parse(snippet("cursor", { name: "x", url }))).toEqual({ mcpServers: { x: { url, headers: { Authorization: `Bearer ${keyPlaceholder}` } } } });
  });

  it("keep client names shell- and JSON-safe", () => {
    expect(clientName("Support tools!", "fallback")).toBe("Support-tools");
    expect(clientName("  ;rm -rf /  ", "fallback")).toBe("rm--rf");
    expect(clientName("", "support-tools")).toBe("support-tools");
  });
});

describe("refused selections", () => {
  const sel = [
    { capabilityId: "a", exposedName: "x__one" },
    { capabilityId: "b", exposedName: "y__one" },
    { capabilityId: "c", exposedName: "x__one" },
  ];

  it("map capability_not_selectable to the capabilities it names", () => {
    const p = selectionProblem(new ApiError(422, "capability_not_selectable", "no", { capabilityIds: ["a", "b"] }), sel);
    expect(p?.ids).toEqual(["a", "b"]);
    expect(p?.message).toMatch(/^2 selected capabilities can't be selected any more/);
  });

  it("map name_clash to the capabilities sharing the name", () => {
    const p = selectionProblem(new ApiError(422, "name_clash", "no", { exposedName: "x__one" }), sel);
    expect(p?.ids).toEqual(["a", "c"]);
    expect(p?.message).toContain("x__one");
  });

  it("say how many to remove when there are too many", () => {
    const many = Array.from({ length: 203 }, (_, i) => ({ capabilityId: String(i) }));
    expect(selectionProblem(new ApiError(422, "too_many", "no"), many)?.message).toBe("A gateway can hold at most 200 capabilities. Remove 3 and save again.");
  });

  it("leave other errors alone", () => {
    expect(selectionProblem(new ApiError(412, "revision_conflict", "no"), sel)).toBeUndefined();
    expect(selectionProblem(new ApiError(422, "something_else", "no"), sel)).toBeUndefined();
    expect(selectionProblem(new Error("x"), sel)).toBeUndefined();
  });
});

describe("picker filtering", () => {
  it("searches names, descriptions, servers and teams, and filters by kind", () => {
    expect(filterOptions(gatewayOptions, "docs team", "").map((o) => o.exposedName)).toEqual(["wiki__search"]);
    expect(filterOptions(gatewayOptions, "", "prompt").map((o) => o.exposedName)).toEqual(["service-status__incident_summary"]);
    expect(filterOptions(gatewayOptions, "", "", { [gatewayOptions[0]!.capabilityId]: "" })).toHaveLength(1);
  });
});

describe("gateway audit words", () => {
  it("name the gateway and API key actions, areas and targets", () => {
    expect(actionLabel("gateway.create")).toBe("Created gateway");
    expect(actionLabel("gateway.capabilities_set")).toBe("Changed gateway capabilities");
    expect(actionLabel("gateway.grant_add")).toBe("Granted gateway access");
    expect(actionLabel("gateway.grant_remove")).toBe("Removed gateway access");
    expect(actionLabel("api_key.create")).toBe("Created API key");
    expect(actionLabel("api_key.revoke")).toBe("Revoked API key");
    expect(areaOf("gateway.update")).toBe("Gateways");
    expect(areaOf("api_key.revoke")).toBe("API keys");
    expect(targetTypeLabel("gateway")).toBe("Gateway");
    expect(targetTypeLabel("api_key")).toBe("API key");
    // Personal keys aren't a team's: their area is only in the platform log.
    expect(areaOptions(false).map((o) => o.value)).not.toContain("api_key.");
    expect(areaOptions(true).map((o) => o.value)).toContain("api_key.");
    expect(areaOptions(false).map((o) => o.value)).toContain("gateway.");
  });
});
