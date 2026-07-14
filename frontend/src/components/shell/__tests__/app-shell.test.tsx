// REDESIGN-001 Phase 4.6 review follow-up — AppShell skip-link contract.
//
// Pins the skip-link wiring that's easy to break in a refactor:
//   1. The first focusable element is the "Skip to main content" anchor.
//   2. Its href resolves to a <main id="main"> in the same tree.
//   3. <main> carries tabIndex={-1} so the anchor can move focus there
//      without inserting <main> into the regular Tab order.
//
// We do NOT stub Sidebar/Topbar/Footer beyond the shell-level mocks they
// need — we want to render the real chrome so an accidental skip-link
// removal/relocation fails this test, not a downstream one.
import { render, screen, act } from "@testing-library/react";
import { describe, test, expect, vi } from "vitest";
import {
  createRouter,
  createMemoryHistory,
  RouterProvider,
  createRoute,
  createRootRoute,
} from "@tanstack/react-router";
import { AppShell } from "../app-shell";

// Hook stubs — same shape as sidebar.test.tsx / topbar.test.tsx.
vi.mock("@/lib/api/workspace", () => ({
  useWorkspace: () => ({ data: { name: "Acme", plan: "pro" } }),
}));
vi.mock("@/lib/api/proxy-cache", () => ({
  useCacheStats: () => ({ data: { total_manifests: 0 } }),
}));
vi.mock("@/lib/auth/store", () => ({
  useAuthStore: (selector: (s: { claims: null }) => unknown) =>
    selector({ claims: null }),
}));
vi.mock("@/lib/api/me", () => ({
  useMe: () => ({ data: { type: "user" } }),
}));
vi.mock("@/lib/api/auth", () => ({
  logout: vi.fn(),
}));
vi.mock("../notifications-bell", () => ({
  NotificationsBell: () => null,
}));
vi.mock("../email-activity-menu", () => ({
  EmailActivityMenu: () => null,
}));
vi.mock("../theme-toggle", () => ({
  ThemeToggle: () => null,
}));
vi.mock("../footer", () => ({
  Footer: () => null,
}));

async function renderAppShell() {
  const rootRoute = createRootRoute({
    component: () => (
      <AppShell>
        <div data-testid="page-content">page</div>
      </AppShell>
    ),
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

describe("AppShell — Phase 4.6 skip-link contract", () => {
  test("renders skip-link with href=#main", async () => {
    await renderAppShell();
    const skip = screen.getByRole("link", { name: "Skip to main content" });
    expect(skip).toBeInTheDocument();
    expect(skip.getAttribute("href")).toBe("#main");
  });

  test("skip-link is the first focusable element in the document", async () => {
    await renderAppShell();
    // querySelector for the FIRST <a> or <button> in document order.
    // Skip-link must come before anything in the Sidebar/Topbar so a
    // single Tab from page load lands there.
    const firstFocusable = document.querySelector("a, button");
    expect(firstFocusable?.textContent).toContain("Skip to main content");
  });

  test("<main> carries id=main and tabIndex=-1", async () => {
    await renderAppShell();
    const main = document.querySelector("main");
    expect(main).not.toBeNull();
    expect(main?.getAttribute("id")).toBe("main");
    // tabIndex={-1} means programmatically focusable, not in Tab order.
    // React renders the JSX number as the DOM attribute string "-1".
    expect(main?.getAttribute("tabindex")).toBe("-1");
  });

  test("page content renders inside <main>", async () => {
    await renderAppShell();
    const main = document.querySelector("main");
    expect(main?.querySelector("[data-testid=page-content]")).not.toBeNull();
  });
});
