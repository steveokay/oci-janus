import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import { ServiceAccountsTable } from "../../access/ServiceAccountsTable";
import type { ServiceAccount } from "@/lib/api/service-accounts";

// Tests for the ServiceAccountsTable badge behaviour (MCP one-click connect,
// Task 5). An SA minted by the MCP flow (origin: "mcp-connect") gets an "MCP"
// badge next to its name with an advisory-scope note; a manually created SA
// does not. useServiceAccounts is mocked so no network is involved.

let mockAccounts: ServiceAccount[] = [];

vi.mock("@/lib/api/service-accounts", async () => {
  const actual = await vi.importActual<
    typeof import("@/lib/api/service-accounts")
  >("@/lib/api/service-accounts");
  return {
    ...actual,
    useServiceAccounts: () => ({
      data: mockAccounts,
      isLoading: false,
      isError: false,
      refetch: vi.fn(),
    }),
  };
});

// fixtureSA — builds a ServiceAccount with sensible defaults so tests only
// have to spell out the fields they care about (name + origin here).
function fixtureSA(overrides: Partial<ServiceAccount> = {}): ServiceAccount {
  return {
    id: `sa-${Math.random().toString(36).slice(2)}`,
    tenant_id: "tenant-1",
    name: "some-account",
    description: "",
    allowed_scopes: ["repo:read"],
    shadow_user_id: "shadow-1",
    created_at: "2026-07-01T00:00:00Z",
    active_key_count: 1,
    origin: "manual",
    ...overrides,
  };
}

describe("ServiceAccountsTable — MCP origin badge", () => {
  beforeEach(() => {
    mockAccounts = [];
  });

  it("badges the mcp-connect SA with 'MCP' and leaves the manual SA unbadged", () => {
    mockAccounts = [
      fixtureSA({ id: "sa-mcp", name: "mcp-agent-xyz", origin: "mcp-connect" }),
      fixtureSA({ id: "sa-manual", name: "ci-bot", origin: "manual" }),
    ];

    render(<ServiceAccountsTable onSelect={vi.fn()} onAdd={vi.fn()} />);

    // Both rows render their account name.
    expect(screen.getByText("mcp-agent-xyz")).toBeInTheDocument();
    expect(screen.getByText("ci-bot")).toBeInTheDocument();

    // Exactly one "MCP" badge is present — for the mcp-connect row only.
    const badges = screen.getAllByText("MCP");
    expect(badges).toHaveLength(1);
  });
});
