import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { PoliciesPanel } from "../PoliciesPanel";
import type { TokenPolicy } from "@/lib/api/token-policy";

// Tests for the live PoliciesPanel (FUT-003 Task 12). Mocks the hooks
// so no network is involved — mirrors the depth pattern established by
// TrustPanel.test.tsx (mutation coverage + client-side validation) so
// this doesn't repeat the shallow-only shape flagged as REM-021.

// Mutable holders per test — reset in beforeEach.
let mockData: TokenPolicy | null = null;
let mockIsLoading = false;
let mockIsError = false;
const mockMutate = vi.fn();

vi.mock("@/lib/api/token-policy", async () => {
  const actual = await vi.importActual<
    typeof import("@/lib/api/token-policy")
  >("@/lib/api/token-policy");
  return {
    ...actual,
    useTokenPolicy: () => ({
      data: mockData,
      isLoading: mockIsLoading,
      isError: mockIsError,
    }),
    usePutTokenPolicy: () => ({
      mutate: mockMutate,
      isPending: false,
    }),
  };
});

// fixturePolicy — builds a TokenPolicy with sensible defaults so tests
// only have to spell out the fields they care about.
function fixturePolicy(overrides: Partial<TokenPolicy> = {}): TokenPolicy {
  return {
    tenant_id: "tenant-1",
    max_ttl_days: 90,
    rotation_interval_days: 365,
    idle_revoke_days: 30,
    updated_at: "2026-07-01T00:00:00Z",
    updated_by_user_id: "user-1",
    ...overrides,
  };
}

// renderPanel — mounts the panel with a fresh QueryClient. Every test
// starts from an empty cache, retry disabled so failures don't loop.
function renderPanel() {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <QueryClientProvider client={qc}>
      <PoliciesPanel />
    </QueryClientProvider>,
  );
}

describe("PoliciesPanel", () => {
  beforeEach(() => {
    mockData = null;
    mockIsLoading = false;
    mockIsError = false;
    mockMutate.mockReset();
  });

  it("renders the heading and does NOT render the amber preview banner", () => {
    mockData = fixturePolicy();
    renderPanel();
    expect(
      screen.getByRole("heading", { name: /token policies/i }),
    ).toBeInTheDocument();
    // The preview kicker is gone now that the surface is live.
    expect(
      screen.queryByText(/Sprint 12.*FUT-003/i),
    ).not.toBeInTheDocument();
  });

  it("renders a loading state while data fetches", () => {
    mockIsLoading = true;
    renderPanel();
    expect(screen.getByRole("status")).toBeInTheDocument();
  });

  it("renders the three policy sections with server values once populated", () => {
    mockData = fixturePolicy({
      max_ttl_days: 90,
      rotation_interval_days: 365,
      idle_revoke_days: 30,
    });
    renderPanel();

    // Each section title is rendered as an h2 heading.
    expect(
      screen.getByRole("heading", { name: /max token ttl/i }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("heading", { name: /force rotation/i }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("heading", { name: /idle revoke/i }),
    ).toBeInTheDocument();

    // Force-rotation description is the new bell-feed copy (email
    // waits on FUT-019).
    expect(
      screen.getByText(/reminder in the bell feed 14 days/i),
    ).toBeInTheDocument();

    // Numeric inputs reflect the server values.
    expect(
      screen.getByLabelText(/max token ttl in days/i),
    ).toHaveValue(90);
    expect(
      screen.getByLabelText(/force rotation interval in days/i),
    ).toHaveValue(365);
    expect(
      screen.getByLabelText(/idle revocation threshold in days/i),
    ).toHaveValue(30);
  });

  it("toggling a section off and saving sends null for that dimension", async () => {
    const user = userEvent.setup();
    mockData = fixturePolicy({
      max_ttl_days: 90,
      rotation_interval_days: 365,
      idle_revoke_days: 30,
    });
    renderPanel();

    // Disable "Idle revoke" — its label toggles to "Enable Idle revoke"
    // when the section is currently enabled. userEvent.click on the
    // checkbox flips it off.
    const idleToggle = screen.getByRole("checkbox", {
      name: /disable idle revoke/i,
    });
    await user.click(idleToggle);

    // Click Save.
    await user.click(screen.getByRole("button", { name: /save/i }));

    await waitFor(() =>
      expect(mockMutate).toHaveBeenCalledWith(
        expect.objectContaining({
          max_ttl_days: 90,
          rotation_interval_days: 365,
          idle_revoke_days: null,
        }),
        expect.any(Object),
      ),
    );
  });

  it("rejects negative days inline before calling the mutation", async () => {
    const user = userEvent.setup();
    mockData = fixturePolicy({ max_ttl_days: 90 });
    renderPanel();

    // Blank the input, then type a negative value.
    const ttlInput = screen.getByLabelText(/max token ttl in days/i);
    await user.clear(ttlInput);
    await user.type(ttlInput, "-5");

    await user.click(screen.getByRole("button", { name: /save/i }));

    // An inline validation banner surfaces + mutation was NOT called.
    expect(screen.getByRole("alert")).toHaveTextContent(
      /max token ttl.*at least 1/i,
    );
    expect(mockMutate).not.toHaveBeenCalled();
  });
});
