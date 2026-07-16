import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react";
import { ConnectedAgentsPanel } from "../ConnectedAgentsPanel";
import type { ServiceAccount } from "@/lib/api/service-accounts";

// Tests for the Connected Agents (MCP) panel (FUT-088 #7). Behaviours:
//   - Only service accounts minted by the one-click MCP connect flow
//     (origin === "mcp-connect") render; "manual" accounts are excluded by
//     the client-side filter.
//   - A row whose last_used_at is null shows "never".
//   - Clicking Revoke opens the confirm dialog; confirming calls the delete
//     mutation with the SA id.
//   - When no mcp-connect accounts remain, the EmptyState renders.
//
// The list + delete hooks are mocked so no network is hit; the pure format
// helper stays real so relative-date rendering genuinely flows through.

// Base fixture shared by the rows below — only the discriminating fields are
// overridden per case so the intent of each row is obvious.
function makeSA(overrides: Partial<ServiceAccount>): ServiceAccount {
  return {
    id: "sa-base",
    tenant_id: "tenant-abc",
    name: "base",
    description: "",
    allowed_scopes: ["repo:read"],
    shadow_user_id: "shadow-1",
    created_at: "2026-07-10T10:00:00Z",
    active_key_count: 1,
    origin: "manual",
    ...overrides,
  };
}

let mockAccounts: ServiceAccount[] = [];
const mutateAsync = vi.fn();

vi.mock("@/lib/api/service-accounts", () => ({
  useServiceAccounts: () => ({
    data: mockAccounts,
    isLoading: false,
    isError: false,
  }),
  useDeleteServiceAccount: () => ({ mutateAsync, isPending: false }),
}));

describe("ConnectedAgentsPanel", () => {
  beforeEach(() => {
    mutateAsync.mockReset();
    mutateAsync.mockResolvedValue(undefined);
    mockAccounts = [
      makeSA({
        id: "mcp-1",
        name: "mcp-agent-used",
        origin: "mcp-connect",
        last_used_at: "2026-07-15T10:00:00Z",
      }),
      makeSA({
        id: "mcp-2",
        name: "mcp-agent-fresh",
        origin: "mcp-connect",
        last_used_at: null,
      }),
      makeSA({
        id: "manual-1",
        name: "manual-ci-bot",
        origin: "manual",
        last_used_at: "2026-07-15T10:00:00Z",
      }),
    ];
  });

  it("renders only the mcp-connect rows and excludes manual accounts", () => {
    render(<ConnectedAgentsPanel />);
    expect(screen.getByText("mcp-agent-used")).toBeInTheDocument();
    expect(screen.getByText("mcp-agent-fresh")).toBeInTheDocument();
    expect(screen.queryByText("manual-ci-bot")).not.toBeInTheDocument();
  });

  it("shows 'never' for a row whose last_used_at is null", () => {
    render(<ConnectedAgentsPanel />);
    const row = screen.getByText("mcp-agent-fresh").closest("tr");
    expect(row).not.toBeNull();
    expect(within(row as HTMLElement).getByText("never")).toBeInTheDocument();
  });

  it("opens the confirm dialog and calls mutateAsync with the SA id on confirm", async () => {
    render(<ConnectedAgentsPanel />);
    const row = screen.getByText("mcp-agent-used").closest("tr");
    fireEvent.click(
      within(row as HTMLElement).getByRole("button", { name: /revoke/i }),
    );

    // Confirm dialog opens.
    const dialog = await screen.findByRole("dialog");
    // severity="medium" gates the confirm button on typing the resource name.
    fireEvent.change(within(dialog).getByRole("textbox"), {
      target: { value: "mcp-agent-used" },
    });
    fireEvent.click(within(dialog).getByRole("button", { name: /revoke/i }));

    await waitFor(() => expect(mutateAsync).toHaveBeenCalledWith("mcp-1"));
  });

  it("renders the EmptyState when no mcp-connect accounts exist", () => {
    mockAccounts = [makeSA({ id: "manual-1", origin: "manual" })];
    render(<ConnectedAgentsPanel />);
    expect(screen.getByText(/no connected agents/i)).toBeInTheDocument();
  });
});
