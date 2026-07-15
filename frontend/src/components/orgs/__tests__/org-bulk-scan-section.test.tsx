import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { OrgBulkScanSection } from "../org-bulk-scan-section";

// Tests for the org-wide bulk-scan action (FUT-088 #5). Behaviours:
//   - Renders a "Scan all repositories" trigger.
//   - Opening the dialog + typing the SCAN confirm phrase enables the button.
//   - Confirming fires useBulkScanOrg with { org } and toasts the result.
//
// The scan API + toast are mocked so no HTTP is touched.

const mockMutate = vi.fn();
let mockPending = false;

vi.mock("@/lib/api/scan", async () => {
  const actual =
    await vi.importActual<typeof import("@/lib/api/scan")>("@/lib/api/scan");
  return {
    ...actual,
    useBulkScanOrg: () => ({
      mutateAsync: mockMutate,
      mutate: mockMutate,
      isPending: mockPending,
      reset: vi.fn(),
    }),
  };
});

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));

function renderSection() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <OrgBulkScanSection org="acme" />
    </QueryClientProvider>,
  );
}

describe("OrgBulkScanSection", () => {
  beforeEach(() => {
    mockMutate.mockReset();
    mockMutate.mockResolvedValue({
      scans_queued: 12,
      repositories_count: 3,
      tags_count: 12,
      capped: false,
      limit: 500,
    });
    mockPending = false;
  });

  it("renders the trigger button", () => {
    renderSection();
    expect(
      screen.getByRole("button", { name: /scan all repositories/i }),
    ).toBeInTheDocument();
  });

  it("gates the confirm on typing SCAN, then fires the mutation", async () => {
    renderSection();
    const user = userEvent.setup();

    // Open the confirm dialog.
    await user.click(
      screen.getByRole("button", { name: /scan all repositories/i }),
    );

    // The confirm button (dialog) is disabled until the phrase is typed.
    const confirmBtn = screen.getByRole("button", { name: /queue scans/i });
    expect(confirmBtn).toBeDisabled();

    await user.type(screen.getByRole("textbox"), "SCAN");
    expect(confirmBtn).toBeEnabled();

    await user.click(confirmBtn);
    expect(mockMutate).toHaveBeenCalledWith({ org: "acme" });
  });
});
