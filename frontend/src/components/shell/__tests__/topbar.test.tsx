// REDESIGN-001 Phase 4.6 review follow-up — Topbar hamburger contract.
//
// Pins two things the broader mobile-nav suite leaves uncovered:
//   1. The hamburger is only rendered when AppShell wires onOpenMobileNav.
//      (Bare Topbar in Storybook / future embedded contexts has no drawer.)
//   2. Clicking it invokes the supplied callback.
//
// We do NOT test the lg:hidden class — that's a media-query concern best
// proven by Chrome DevTools mobile emulation in manual QA, not unit tests.
import { render, screen, act } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, test, expect, vi } from "vitest";
import {
  createRouter,
  createMemoryHistory,
  RouterProvider,
  createRoute,
  createRootRoute,
} from "@tanstack/react-router";
import { Topbar } from "../topbar";

// ── Module mocks — Topbar reaches useAuthStore + useMe + logout fn ───────

vi.mock("@/lib/auth/store", () => ({
  useAuthStore: (selector: (s: { claims: { sub?: string; tenant_id?: string; username?: string } | null }) => unknown) =>
    selector({ claims: { sub: "u-1", tenant_id: "t-uuid-123", username: "alice" } }),
}));

vi.mock("@/lib/api/me", () => ({
  useMe: () => ({ data: { type: "user" } }),
}));

vi.mock("@/lib/api/auth", () => ({
  logout: vi.fn().mockResolvedValue(undefined),
}));

// NotificationsBell + ThemeToggle pull their own hooks; stub them so we
// don't have to mount their full dependency trees.
vi.mock("../notifications-bell", () => ({
  NotificationsBell: () => null,
}));

vi.mock("../email-activity-menu", () => ({
  EmailActivityMenu: () => null,
}));

vi.mock("../theme-toggle", () => ({
  ThemeToggle: () => null,
}));

async function renderTopbar(onOpenMobileNav?: () => void) {
  const rootRoute = createRootRoute({
    component: () => <Topbar onOpenMobileNav={onOpenMobileNav} />,
  });
  const indexRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: "/",
    component: () => null,
  });
  const routeTree = rootRoute.addChildren([indexRoute]);
  const router = createRouter({
    routeTree,
    history: createMemoryHistory({ initialEntries: ["/"] }),
  });
  await router.load();
  await act(async () => {
    render(<RouterProvider router={router} />);
  });
}

describe("Topbar — Phase 4.6 hamburger contract", () => {
  test("no hamburger when onOpenMobileNav prop is omitted", async () => {
    await renderTopbar(undefined);
    expect(
      screen.queryByRole("button", { name: "Open navigation" }),
    ).not.toBeInTheDocument();
  });

  test("hamburger renders and fires callback when prop supplied", async () => {
    const onOpen = vi.fn();
    await renderTopbar(onOpen);
    const hamburger = screen.getByRole("button", { name: "Open navigation" });
    expect(hamburger).toBeInTheDocument();
    const user = userEvent.setup();
    await user.click(hamburger);
    expect(onOpen).toHaveBeenCalledTimes(1);
  });
});

// Single-tenant only (ADR-0031): the tenant UUID chip was removed from the
// avatar dropdown entirely — surfacing the single fixed tenant id is
// meaningless chrome. This pins that it no longer renders.
describe("Topbar — no tenant UUID chip (single-tenant)", () => {
  test("the avatar dropdown does not show the tenant id", async () => {
    await renderTopbar();
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: /alice/i }));
    expect(screen.queryByText("t-uuid-123")).not.toBeInTheDocument();
  });
});
