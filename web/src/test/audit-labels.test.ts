/* Audit words: the initial platform role, humanized roles, and "Not set" in both the list and the record. */
import { auditChange, changedRows, changeSummary, valueText } from "../components/audit/changes";
import { actionLabel, areaOf, targetTypeLabel } from "../components/audit/labels";

describe("audit labels", () => {
  it("names the bootstrap grant neutrally and shows its role in words", () => {
    const e = { action: "platform.bootstrap_role", before: { platformRole: "none" }, after: { platformRole: "platform_admin" } };
    expect(actionLabel(e.action)).toBe("Granted initial platform role");
    expect(changeSummary(e)).toBe("Platform role None → Platform admin");
    expect(changedRows(auditChange(e))).toEqual([{ field: "Platform role", before: "None", after: "Platform admin" }]);
  });

  it("says Not set for a missing value in the list, as the record does", () => {
    const e = { action: "team.update", before: { description: "" }, after: { description: "Tools" } };
    expect(changeSummary(e)).toBe("Description Not set → Tools");
    expect(valueText(undefined)).toBe("Not set");
  });

  it("knows the registry's target types", () => {
    expect(targetTypeLabel("mcp_server")).toBe("MCP server");
    expect(targetTypeLabel("mcp_capability")).toBe("Capability");
  });

  it("names the registry's actions and areas", () => {
    expect(actionLabel("mcp_server.publish_request")).toBe("Requested catalog review");
    expect(actionLabel("mcp_capability.approve")).toBe("Approved capability");
    expect(areaOf("mcp_server.refresh")).toBe("MCP servers");
    expect(areaOf("settings.publishing_update")).toBe("Platform settings");
  });
});
