import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { ReviewPanel } from "../ReviewPanel";
import type { StaleKey } from "@/lib/api/access-review";

// Tests for the live ReviewPanel (FUT-004 Task 10). Mocks the hooks
// so no network is involved — mirrors the depth pattern established
// by PoliciesPanel.test.tsx / TrustPanel.test.tsx (mocked hooks +
// userEvent + suggested-action visual assertion).

// Mutable holders per test.
let mockData: StaleKey[] | null = null;
let mockIsLoading = false;
let mockIsError = false;
const mockSnooze = vi.fn();
const mockRevoke = vi.fn();

vi.mock("@/lib/api/access-review", async () => {
  const actual = await vi.importActual<
    typeof import("@/lib/api/access-review")
  >("@/lib/api/access-review");
  return {
    ...actual,
    useStaleKeys: () => ({
      data: mockData,
      isLoading: mockIsLoading,
      isError: mockIsError,
    }),
    useSnoozeKey: () => ({
      mutate: mockSnooze,
      isPending: false,
    }),
  };
});

vi.mock("@/lib/api/api-keys", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api/api-keys")>(
    "@/lib/api/api-keys",
  );
  return {
    ...actual,
    useDeleteApiKey: () => ({
      mutate: mockRevoke,
      isPending: false,
    }),
  };
});

// sonner toast is called on success/error; mock it so tests don't try to
// render toast markup and so we can assert it was hit if we ever want to.
vi.mock("sonner", () => ({
  toast: {
    success: vi.fn(),
    error: vi.fn(),
  },
}));

// fixtureKey — builds a StaleKey with sensible defaults so each test
// only spells out the fields it cares about.
function fixtureKey(overrides: Partial<StaleKey> = {}): StaleKey {
  return {
    id: "key-1",
    tenant_id: "tenant-1",
    owner_user_id: "user-1",
    name: "deploy-prod",
    last_used_at: "2026-05-01T00:00:00Z",
    rotation_due_at: null,
    review_snoozed_until: null,
    suggested_action: "REVOKE",
    reason: "idle",
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
      <ReviewPanel />
    </QueryClientProvider>,
  );
}

describe("ReviewPanel", () => {
  beforeEach(() => {
    mockData = null;
    mockIsLoading = false;
    mockIsError = false;
    mockSnooze.mockReset();
    mockRevoke.mockReset();
  });

  it("renders the heading and does NOT render the amber preview banner", () => {
    mockData = [];
    renderPanel();
    expect(
      screen.getByRole("heading", { name: /access review/i }),
    ).toBeInTheDocument();
    // The Sprint-12 preview banner is gone now that the surface is live.
    expect(
      screen.queryByText(/Sprint 12.*FUT-004/i),
    ).not.toBeInTheDocument();
  });

  it("renders a loading state while data fetches", () => {
    mockIsLoading = true;
    renderPanel();
    expect(screen.getByRole("status")).toBeInTheDocument();
  });

  it("renders the empty state when there are no stale keys", () => {
    mockData = [];
    renderPanel();
    expect(
      screen.getByText(/nothing to review today/i),
    ).toBeInTheDocument();
  });

  it("renders the table with N rows + amber banner when populated", () => {
    mockData = [
      fixtureKey({ id: "k1", name: "deploy-prod" }),
      fixtureKey({
        id: "k2",
        name: "ci-legacy",
        owner_user_id: "user-2",
        reason: "rotation_lapsed",
        suggested_action: "SNOOZE",
      }),
      fixtureKey({
        id: "k3",
        name: "terraform-dev",
        owner_user_id: "user-3",
        reason: "both",
        suggested_action: "KEEP",
      }),
    ];
    renderPanel();

    // Amber banner shows the count.
    expect(screen.getByRole("alert")).toHaveTextContent(
      /3 keys due for review/i,
    );

    // Every row's key name is rendered.
    expect(screen.getByText("deploy-prod")).toBeInTheDocument();
    expect(screen.getByText("ci-legacy")).toBeInTheDocument();
    expect(screen.getByText("terraform-dev")).toBeInTheDocument();
  });

  it("snooze button calls the mutation with days: 30", async () => {
    const user = userEvent.setup();
    mockData = [fixtureKey({ id: "k1", name: "deploy-prod" })];
    renderPanel();

    // Two of the three action rows have a "Snooze 30d" button; there's
    // only one row so getByRole is unambiguous.
    await user.click(
      screen.getByRole("button", { name: /snooze 30d/i }),
    );

    await waitFor(() =>
      expect(mockSnooze).toHaveBeenCalledWith(
        expect.objectContaining({ key_id: "k1", days: 30 }),
        expect.any(Object),
      ),
    );
  });

  it("revoke button calls the revoke mutation with the key id", async () => {
    const user = userEvent.setup();
    mockData = [fixtureKey({ id: "k1", name: "deploy-prod" })];
    renderPanel();

    await user.click(screen.getByRole("button", { name: /revoke/i }));

    await waitFor(() =>
      expect(mockRevoke).toHaveBeenCalledWith(
        "k1",
        expect.any(Object),
      ),
    );
  });

  it("Keep button drops the row locally without calling any mutation", async () => {
    const user = userEvent.setup();
    mockData = [
      fixtureKey({ id: "k1", name: "deploy-prod" }),
      fixtureKey({ id: "k2", name: "ci-legacy" }),
    ];
    renderPanel();

    // Both rows initially visible.
    expect(screen.getByText("deploy-prod")).toBeInTheDocument();
    expect(screen.getByText("ci-legacy")).toBeInTheDocument();

    // Click Keep on the first row. There are now two "Keep" buttons —
    // use `getAllByRole` and pick the first.
    const keepButtons = screen.getAllByRole("button", { name: /^keep$/i });
    await user.click(keepButtons[0]);

    // First row falls off; second row remains.
    await waitFor(() =>
      expect(screen.queryByText("deploy-prod")).not.toBeInTheDocument(),
    );
    expect(screen.getByText("ci-legacy")).toBeInTheDocument();

    // Neither backend mutation was called.
    expect(mockSnooze).not.toHaveBeenCalled();
    expect(mockRevoke).not.toHaveBeenCalled();
  });

  it("REVOKE suggested-action row emphasises the Revoke button", () => {
    mockData = [
      fixtureKey({
        id: "k1",
        name: "deploy-prod",
        suggested_action: "REVOKE",
      }),
    ];
    renderPanel();

    // Revoke button carries data-suggested="true" when it's the
    // suggested action; the other two buttons don't.
    const revokeButton = screen.getByRole("button", { name: /revoke/i });
    expect(revokeButton).toHaveAttribute("data-suggested", "true");

    const keepButton = screen.getByRole("button", { name: /^keep$/i });
    expect(keepButton).not.toHaveAttribute("data-suggested");

    const snoozeButton = screen.getByRole("button", {
      name: /snooze 30d/i,
    });
    expect(snoozeButton).not.toHaveAttribute("data-suggested");
  });
});
