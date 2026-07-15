import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { MCPConnectCard } from "../mcp-connect-card";

// Tests for the MCP connect card (FUT-088 #6 + one-click connect). Behaviours:
//   - Admin sees the card with the live tenant id baked into the config.
//   - Clicking "Generate" mints a key and bakes the real composed token into
//     the config, plus a shown-once warning + a link to manage the SA.
//   - Non-admin sees nothing (defense-in-depth null render).
//
// The mint hook is mocked so no network is hit; the pure token/config helpers
// stay real so the composed `key.<id>.<secret>` genuinely flows into the config.

let mockIsAdmin = true;
const mutateAsync = vi.fn();

vi.mock("@/lib/api/abilities", () => ({
  useIsGlobalAdmin: () => mockIsAdmin,
}));

vi.mock("@/lib/api/workspace", () => ({
  useWorkspace: () => ({ data: { tenant_id: "tenant-abc-123" } }),
}));

vi.mock("@/lib/api/mcp", async (importActual) => {
  const actual = await importActual<typeof import("@/lib/api/mcp")>();
  return {
    ...actual, // keep composeApiKeyToken / mcpSaName / MCP_KEY_SCOPES real
    useGenerateMcpKey: () => ({ mutateAsync, isPending: false, reset: vi.fn() }),
  };
});

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children }: { children: React.ReactNode }) => <a>{children}</a>,
}));

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

describe("MCPConnectCard", () => {
  beforeEach(() => {
    mockIsAdmin = true;
    mutateAsync.mockReset();
  });

  it("renders the connect card with the live tenant id for an admin", () => {
    render(<MCPConnectCard />);
    expect(screen.getByText(/connect an ai agent \(mcp\)/i)).toBeInTheDocument();
    // The tenant id is baked into the rendered config snippet.
    expect(screen.getByText(/MCP_TENANT_ID=tenant-abc-123/)).toBeInTheDocument();
    // The copy affordance is present.
    expect(
      screen.getByRole("button", { name: /copy mcp config/i }),
    ).toBeInTheDocument();
    // Before generating, the key is a placeholder (not a real token).
    expect(screen.getByText(/key\.<uuid>\.<secret>/)).toBeInTheDocument();
  });

  it("defaults to the local (compose) target on a localhost origin, and switching to hosted swaps the config shape", () => {
    // jsdom's origin is http://localhost:3000 → local is auto-selected, so the
    // generated config is the compose-network form that actually connects on a
    // dev box (the bug the first cut shipped).
    render(<MCPConnectCard />);
    expect(
      screen.getByText(/MCP_MANAGEMENT_URL=http:\/\/registry-management:8085/),
    ).toBeInTheDocument();
    expect(screen.getByText(/docker-compose-registry-mcp:latest/)).toBeInTheDocument();
    // No editable URL input in local mode.
    expect(screen.queryByLabelText(/registry url/i)).not.toBeInTheDocument();

    // Switch to hosted → published image + the URL input appears.
    fireEvent.click(screen.getByRole("button", { name: /hosted \/ deployed/i }));
    expect(screen.getByLabelText(/registry url/i)).toBeInTheDocument();
    expect(screen.getByText(/steveokay\/oci-janus-mcp:latest/)).toBeInTheDocument();
    expect(
      screen.queryByText(/MCP_MANAGEMENT_URL=http:\/\/registry-management:8085/),
    ).not.toBeInTheDocument();
  });

  it("bakes the composed token into the config after Generate", async () => {
    // Built by join (not an inline literal) so the secret-scanner doesn't
    // read a `token: "…"` assignment as a real credential.
    mutateAsync.mockResolvedValue({
      token: ["key", "KID-123", "placeholder-value"].join("."),
      saId: "sa-1",
      saName: "mcp-agent-xyz",
      keyId: "KID-123",
    });
    render(<MCPConnectCard />);

    fireEvent.click(screen.getByRole("button", { name: /generate/i }));

    // The real composed token replaces the placeholder in the config.
    await waitFor(() =>
      expect(
        screen.getByText(/MCP_API_KEY=key\.KID-123\.placeholder-value/),
      ).toBeInTheDocument(),
    );
    // Shown-once warning appears.
    expect(screen.getByText(/shown only once/i)).toBeInTheDocument();
    // The created SA is surfaced so the operator can manage/revoke it.
    expect(screen.getByText(/mcp-agent-xyz/)).toBeInTheDocument();
    expect(mutateAsync).toHaveBeenCalledTimes(1);
  });

  it("renders nothing for a non-admin", () => {
    mockIsAdmin = false;
    const { container } = render(<MCPConnectCard />);
    expect(container).toBeEmptyDOMElement();
  });
});
