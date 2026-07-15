import { describe, it, expect } from "vitest";
import {
  buildStdioConfig,
  detectDefaultTarget,
  LOCAL_COMPOSE_NETWORK,
  LOCAL_MANAGEMENT_URL,
  LOCAL_MCP_IMAGE,
  HOSTED_MCP_IMAGE,
} from "@/lib/mcp-config";
import { composeApiKeyToken, mcpSaName } from "@/lib/api/mcp";

// Pure helpers behind the one-click MCP connect flow. These encode the two
// things a user otherwise has to know by hand: the `key.<id>.<secret>` Bearer
// composition (parseAPIKeyBearer in services/auth) and the claude_desktop
// config shape — including the local-Docker-Compose vs hosted split that made
// the first cut of the generated config fail to connect on a dev box.

describe("composeApiKeyToken", () => {
  it("assembles the key.<keyId>.<secret> bearer form", () => {
    expect(
      composeApiKeyToken("11111111-1111-1111-1111-111111111111", "deadbeef"),
    ).toBe("key.11111111-1111-1111-1111-111111111111.deadbeef");
  });

  it("uses the API key's own id, not the service-account id", () => {
    const token = composeApiKeyToken("KEY-ID", "SECRET");
    expect(token).toBe("key.KEY-ID.SECRET");
    expect(token.split(".")).toHaveLength(3);
  });
});

describe("mcpSaName", () => {
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

describe("detectDefaultTarget", () => {
  it("selects local for a localhost / loopback origin", () => {
    expect(detectDefaultTarget("http://localhost:3000")).toBe("local");
    expect(detectDefaultTarget("http://127.0.0.1:3000")).toBe("local");
    expect(detectDefaultTarget("http://localhost")).toBe("local");
  });

  it("selects hosted for a real origin", () => {
    expect(detectDefaultTarget("https://registry.example.com")).toBe("hosted");
    expect(detectDefaultTarget("https://reg.internal.corp:8443")).toBe("hosted");
  });
});

describe("buildStdioConfig — local (Docker Compose)", () => {
  it("emits a config that actually connects on a compose dev box", () => {
    const cfg = buildStdioConfig({
      target: "local",
      tenantID: "tenant-abc-123",
      managementURL: "http://localhost:3000", // ignored in local mode
      apiKey: "key.KID.SECRET",
    });
    // Joins the compose network so the container can resolve the BFF.
    expect(cfg).toContain(`"--network"`);
    expect(cfg).toContain(`"${LOCAL_COMPOSE_NETWORK}"`);
    // Internal service DNS + BFF port — NOT the browser origin / SPA port.
    expect(cfg).toContain(`MCP_MANAGEMENT_URL=${LOCAL_MANAGEMENT_URL}`);
    expect(cfg).not.toContain("localhost:3000");
    // The locally-built compose image, not the (unpublished) hosted one.
    expect(cfg).toContain(LOCAL_MCP_IMAGE);
    expect(cfg).not.toContain(HOSTED_MCP_IMAGE);
    expect(cfg).toContain("MCP_API_KEY=key.KID.SECRET");
    expect(cfg).toContain("MCP_TENANT_ID=tenant-abc-123");
    expect(() => JSON.parse(cfg)).not.toThrow();
  });
});

describe("buildStdioConfig — hosted", () => {
  it("bakes in the real token, tenant id, and origin URL", () => {
    const cfg = buildStdioConfig({
      target: "hosted",
      tenantID: "tenant-abc-123",
      managementURL: "https://reg.example.com",
      apiKey: "key.KID.SECRET",
    });
    expect(cfg).toContain("MCP_API_KEY=key.KID.SECRET");
    expect(cfg).toContain("MCP_TENANT_ID=tenant-abc-123");
    expect(cfg).toContain("MCP_MANAGEMENT_URL=https://reg.example.com");
    expect(cfg).toContain(HOSTED_MCP_IMAGE);
    // No compose network in hosted mode.
    expect(cfg).not.toContain("--network");
    expect(cfg).toContain("MCP_TRANSPORT=stdio");
    expect(() => JSON.parse(cfg)).not.toThrow();
  });

  it("falls back to placeholders when fields are empty", () => {
    const cfg = buildStdioConfig({
      target: "hosted",
      tenantID: "",
      managementURL: "",
      apiKey: "",
    });
    expect(cfg).toContain("key.<uuid>.<secret>");
    expect(cfg).toContain("<tenant-id>");
    expect(() => JSON.parse(cfg)).not.toThrow();
  });
});
