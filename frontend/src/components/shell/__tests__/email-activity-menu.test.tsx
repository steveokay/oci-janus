// FUT-019 Phase 3 — EmailActivityMenu contract.
//
// Pins the three behaviours the topbar mail-icon depends on:
//   1. The Mail trigger button renders (aria-label "Email activity").
//   2. Opening the popover shows a delivery row from mocked useEmailDeliveries.
//   3. The empty state ("No emails yet") shows when there are no deliveries.
//
// Both data hooks are mocked so the test never touches react-query / the
// apiClient — the component under test is the presentation shell only.
import { render, screen, act } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, test, expect, vi, beforeEach } from "vitest";
import {
  createRouter,
  createMemoryHistory,
  RouterProvider,
  createRoute,
  createRootRoute,
} from "@tanstack/react-router";
import { EmailActivityMenu } from "../email-activity-menu";
import type { EmailDeliveriesPage } from "@/lib/api/email-deliveries";

// ── Module mocks ─────────────────────────────────────────────────────────
// useEmailDeliveries is swapped per test via the factory so each case can
// pick loading / populated / empty. useEmailTransport is stubbed to the
// non-admin 403 posture (isError) so the empty state stays "No emails yet".
type DeliveriesResult = {
  data: EmailDeliveriesPage | undefined;
  isError: boolean;
  refetch: () => void;
};
const mockDeliveries = vi.fn(
  (): DeliveriesResult => ({ data: { deliveries: [] }, isError: false, refetch: vi.fn() }),
);

vi.mock("@/lib/api/email-deliveries", () => ({
  useEmailDeliveries: () => mockDeliveries(),
}));

vi.mock("@/lib/api/email-transport", () => ({
  // Non-admins get a 403 → isError true → component treats enabled as
  // "unknown" and shows the plain "No emails yet" empty state.
  useEmailTransport: () => ({ data: undefined, isError: true }),
}));

async function renderMenu() {
  const rootRoute = createRootRoute({ component: () => <EmailActivityMenu /> });
  const indexRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: "/",
    component: () => null,
  });
  // The footer link targets /settings/notifications — register a stub route
  // so the router can resolve it without warning.
  const settingsRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: "/settings/notifications",
    component: () => null,
  });
  const routeTree = rootRoute.addChildren([indexRoute, settingsRoute]);
  const router = createRouter({
    routeTree,
    history: createMemoryHistory({ initialEntries: ["/"] }),
  });
  await router.load();
  await act(async () => {
    render(<RouterProvider router={router} />);
  });
}

describe("EmailActivityMenu", () => {
  beforeEach(() => {
    mockDeliveries.mockReset();
  });

  test("renders the Mail trigger button", async () => {
    mockDeliveries.mockReturnValue({
      data: { deliveries: [] },
      isError: false,
      refetch: vi.fn(),
    });
    await renderMenu();
    expect(
      screen.getByRole("button", { name: "Email activity" }),
    ).toBeInTheDocument();
  });

  test("opening the panel shows a delivery row", async () => {
    mockDeliveries.mockReturnValue({
      data: {
        deliveries: [
          {
            id: "d-1",
            category: "scan.policy_blocked",
            subject: "Scan blocked alpine:3.20",
            to_address: "alice@example.com",
            status: "sent",
            last_error: "",
            created_at: "2026-07-07T10:00:00Z",
            sent_at: "2026-07-07T10:00:01Z",
          },
        ],
      },
      isError: false,
      refetch: vi.fn(),
    });
    await renderMenu();
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Email activity" }));
    expect(screen.getByText("Scan blocked alpine:3.20")).toBeInTheDocument();
    expect(screen.getByText("alice@example.com")).toBeInTheDocument();
    expect(screen.getByText("Sent")).toBeInTheDocument();
  });

  test("shows the empty state when there are no deliveries", async () => {
    mockDeliveries.mockReturnValue({
      data: { deliveries: [] },
      isError: false,
      refetch: vi.fn(),
    });
    await renderMenu();
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Email activity" }));
    expect(screen.getByText("No emails yet")).toBeInTheDocument();
  });
});
