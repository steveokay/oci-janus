import { describe, it, expect } from "vitest";
import { buildStdioConfig } from "@/lib/mcp-config";
import { composeApiKeyToken, mcpSaName } from "@/lib/api/mcp";

// Pure helpers behind the one-click MCP connect flow. These encode the two
// things a user otherwise has to know by hand: the `key.<id>.<secret>` Bearer
// composition (parseAPIKeyBearer in services/auth) and the claude_desktop
// config shape.

describe("composeApiKeyToken", () => {
  it("assembles the key.<keyId>.<secret> bearer form", () => {
    expect(
      composeApiKeyToken("11111111-1111-1111-1111-111111111111", "deadbeef"),
    ).toBe("key.11111111-1111-1111-1111-111111111111.deadbeef");
  });

  it("uses the API key's own id, not the service-account id", () => {
    // Guards the exact confusion that motivated this feature: the middle
    // segment must be the KEY id (returned by the issue-key call), never the SA
    // id or the shadow_user_id.
    const token = composeApiKeyToken("KEY-ID", "SECRET");
    expect(token).toBe("key.KEY-ID.SECRET");
    expect(token.split(".")).toHaveLength(3);
  });
});

describe("mcpSaName", () => {
  // The generated service-account name must satisfy the auth handler's regex
  // (^[a-z0-9]+([._-][a-z0-9]+)*$) or the mint call 400s.
  const SA_NAME_RE = /^[a-z0-9]+([._-][a-z0-9]+)*$/;

  it("produces a name valid against the SA name regex", () => {
    const name = mcpSaName(1752600000000);
    expect(name).toMatch(SA_NAME_RE);
    expect(name.startsWith("mcp-agent-")).toBe(true);
  });

  it("produces distinct names for distinct timestamps (no repeat-click collision)", () => {
    expect(mcpSaName(1)).not.toBe(mcpSaName(2));
  });
});

describe("buildStdioConfig", () => {
  it("bakes in the real token, tenant id, and url when provided", () => {
    const cfg = buildStdioConfig({
      tenantID: "tenant-abc-123",
      managementURL: "https://reg.example.com",
      apiKey: "key.KID.SECRET",
    });
    expect(cfg).toContain("MCP_API_KEY=key.KID.SECRET");
    expect(cfg).toContain("MCP_TENANT_ID=tenant-abc-123");
    expect(cfg).toContain("MCP_MANAGEMENT_URL=https://reg.example.com");
    expect(cfg).toContain("MCP_TRANSPORT=stdio");
    // Must always be valid JSON so it can be pasted directly.
    expect(() => JSON.parse(cfg)).not.toThrow();
  });

  it("falls back to placeholders when fields are empty", () => {
    const cfg = buildStdioConfig({ tenantID: "", managementURL: "", apiKey: "" });
    expect(cfg).toContain("key.<uuid>.<secret>");
    expect(cfg).toContain("<tenant-id>");
    expect(() => JSON.parse(cfg)).not.toThrow();
  });
});
