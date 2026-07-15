import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { MCPConnectCard } from "../mcp-connect-card";

// Tests for the MCP connect card (FUT-088 #6). Behaviours:
//   - Admin sees the card with the live tenant id baked into the config.
//   - Non-admin sees nothing (defense-in-depth null render).
//
// abilities + workspace hooks are mocked; the admin flag is swapped per test
// via the mutable `mockIsAdmin`. @tanstack/react-router's Link is stubbed so
// the component renders without a router context.

let mockIsAdmin = true;

vi.mock("@/lib/api/abilities", () => ({
  useIsGlobalAdmin: () => mockIsAdmin,
}));

vi.mock("@/lib/api/workspace", () => ({
  useWorkspace: () => ({ data: { tenant_id: "tenant-abc-123" } }),
}));

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children }: { children: React.ReactNode }) => <a>{children}</a>,
}));

describe("MCPConnectCard", () => {
  it("renders the connect card with the live tenant id for an admin", () => {
    mockIsAdmin = true;
    render(<MCPConnectCard />);
    expect(screen.getByText(/connect an ai agent \(mcp\)/i)).toBeInTheDocument();
    // The tenant id is baked into the rendered config snippet.
    expect(
      screen.getByText(/MCP_TENANT_ID=tenant-abc-123/),
    ).toBeInTheDocument();
    // The copy affordance is present.
    expect(
      screen.getByRole("button", { name: /copy mcp config/i }),
    ).toBeInTheDocument();
  });

  it("renders nothing for a non-admin", () => {
    mockIsAdmin = false;
    const { container } = render(<MCPConnectCard />);
    expect(container).toBeEmptyDOMElement();
  });
});
