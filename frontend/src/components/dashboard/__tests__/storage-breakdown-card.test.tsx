import { render, screen } from "@testing-library/react";
import { describe, test, expect, vi, beforeEach } from "vitest";
import { StorageBreakdownCard } from "../storage-breakdown-card";
import type { StorageBreakdownResponse } from "@/lib/api/stats-storage";

// ---------------------------------------------------------------------------
// StorageBreakdownCard renders the tenant storage breakdown. These tests
// focus on the REM-013 gap 3 addition: the tenant-wide "Reclaimed via
// retention" stat, which surfaces retention_reclaimed_bytes from the BFF and
// renders "—" when it's 0 or absent (GC service unwired / no retention run).
//
// The card fetches through useStorageBreakdown; we mock the hook so the tests
// drive the exact response shape without a QueryClient / network.
// ---------------------------------------------------------------------------

const mockUseStorageBreakdown = vi.fn();

vi.mock("@/lib/api/stats-storage", () => ({
  useStorageBreakdown: () => mockUseStorageBreakdown(),
}));

// TanStack Router's <Link> needs a router context; stub it to a plain anchor
// so the card renders standalone.
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children }: { children: React.ReactNode }) => <a>{children}</a>,
}));

function loaded(data: StorageBreakdownResponse) {
  return { data, isLoading: false, isError: false, refetch: vi.fn() };
}

const baseData: StorageBreakdownResponse = {
  tenant_storage_used_bytes: 1500,
  tenant_storage_quota_bytes: 0,
  repositories: [
    {
      repo_id: "r1",
      org: "acme",
      name: "api",
      storage_used_bytes: 1000,
      percent_of_tenant: 66.667,
      // Give the row a policy summary so its per-row "Retention" cell renders
      // the summary (not "—") — that keeps the tenant-level "Reclaimed via
      // retention: —" the only "—" on the card, so the assertions below are
      // unambiguous.
      retention_summary: "30d age",
      retention_source: "repo",
    },
  ],
};

describe("StorageBreakdownCard — retention savings (REM-013 gap 3)", () => {
  beforeEach(() => {
    mockUseStorageBreakdown.mockReset();
  });

  test("renders reclaimed bytes when retention_reclaimed_bytes > 0", () => {
    mockUseStorageBreakdown.mockReturnValue(
      loaded({ ...baseData, retention_reclaimed_bytes: 4096 }),
    );
    render(<StorageBreakdownCard />);
    expect(screen.getByText("Reclaimed via retention")).toBeInTheDocument();
    // 4096 bytes → "4.0 KB" (formatBytes: 1024 base, 1 decimal at KB).
    expect(screen.getByText("4.0 KB")).toBeInTheDocument();
  });

  test("renders — when retention_reclaimed_bytes is 0", () => {
    mockUseStorageBreakdown.mockReturnValue(
      loaded({ ...baseData, retention_reclaimed_bytes: 0 }),
    );
    render(<StorageBreakdownCard />);
    expect(screen.getByText("Reclaimed via retention")).toBeInTheDocument();
    expect(screen.getByText("—")).toBeInTheDocument();
  });

  test("renders — when the field is absent (GC unwired)", () => {
    mockUseStorageBreakdown.mockReturnValue(loaded(baseData));
    render(<StorageBreakdownCard />);
    expect(screen.getByText("Reclaimed via retention")).toBeInTheDocument();
    expect(screen.getByText("—")).toBeInTheDocument();
  });
});
