// REDESIGN-001 Phase 4.6 Task B — mobile drawer behaviour tests.
//
// MobileNav wraps SidebarBody in a Radix Dialog. Radix gives us
// focus-trap, ESC-to-close, outside-click, and ARIA roles for free —
// we don't re-test Radix internals. What we DO pin:
//
//   1. open=false renders nothing in the portal
//   2. open=true mounts the dialog with role=dialog + an accessible name
//   3. clicking a nav Link fires onOpenChange(false) so the drawer
//      dismisses on link tap (the load-bearing UX requirement for the
//      one-tap dismiss pattern)
//   4. the explicit close button (X) fires onOpenChange(false)
//   5. ESC fires onOpenChange(false) — light smoke test that Radix is
//      actually wired (catches a "forgot the Dialog.Root" regression)
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
import { MobileNav } from "../mobile-nav";

// ── Module mocks — same shape as sidebar.test.tsx ────────────────────────

vi.mock("@/lib/api/workspace", () => ({
  useWorkspace: () => ({ data: { name: "Acme", plan: "pro" } }),
}));

const mockUseCacheStats = vi.fn(() => ({ data: { total_manifests: 0 } as unknown }));
vi.mock("@/lib/api/proxy-cache", () => ({
  useCacheStats: () => mockUseCacheStats(),
}));

vi.mock("@/lib/api/deployment-info", () => ({
  useDeploymentInfo: () => ({ data: { deployment_mode: "single", version: "dev" } }),
}));

vi.mock("@/lib/auth/store", () => ({
  useAuthStore: (selector: (s: { claims: null }) => unknown) =>
    selector({ claims: null }),
}));

// ── Render helper — mount MobileNav inside a minimal router so the
// SidebarBody's TanStack <Link> elements have a router context. ─────────

interface RenderArgs {
  open?: boolean;
  onOpenChange?: (open: boolean) => void;
  initialPath?: string;
}

async function renderMobileNav({
  open = true,
  onOpenChange = () => undefined,
  initialPath = "/",
}: RenderArgs = {}) {
  const rootRoute = createRootRoute({
    component: () => (
      <MobileNav open={open} onOpenChange={onOpenChange} />
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
    history: createMemoryHistory({ initialEntries: [initialPath] }),
  });
  await router.load();
  await act(async () => {
    render(<RouterProvider router={router} />);
  });
}

describe("MobileNav — Phase 4.6 drawer behaviour", () => {
  beforeEach(() => {
    mockUseCacheStats.mockReset();
    mockUseCacheStats.mockReturnValue({ data: { total_manifests: 0 } });
  });

  test("open=false renders no dialog in the portal", async () => {
    await renderMobileNav({ open: false });
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  test("open=true mounts a dialog with the Title as accessible name", async () => {
    await renderMobileNav({ open: true });
    const dialog = screen.getByRole("dialog");
    expect(dialog).toBeInTheDocument();
    // Radix wires aria-labelledby to Dialog.Title. The Title text is
    // visually hidden via sr-only but available to assistive tech.
    expect(dialog).toHaveAccessibleName("Navigation");
  });

  test("clicking a nav Link fires onOpenChange(false) for one-tap dismiss", async () => {
    const onOpenChange = vi.fn();
    await renderMobileNav({ open: true, onOpenChange });

    // SidebarBody renders the "Repositories" Link from GROUPS. The mobile
    // wrapper passes `onNavigate = () => onOpenChange(false)` so any link
    // click should bubble back through this callback. Use userEvent so we
    // exercise the real click pipeline (not a synthetic event).
    const user = userEvent.setup();
    const repositories = screen.getByText("Repositories");
    await user.click(repositories);

    expect(onOpenChange).toHaveBeenCalledWith(false);
  });

  test("explicit close button dismisses the drawer", async () => {
    const onOpenChange = vi.fn();
    await renderMobileNav({ open: true, onOpenChange });
    const user = userEvent.setup();

    const closeBtn = screen.getByRole("button", { name: "Close navigation" });
    await user.click(closeBtn);

    expect(onOpenChange).toHaveBeenCalledWith(false);
  });

  test("ESC key fires onOpenChange(false) (Radix wiring smoke test)", async () => {
    const onOpenChange = vi.fn();
    await renderMobileNav({ open: true, onOpenChange });
    const user = userEvent.setup();

    await user.keyboard("{Escape}");

    expect(onOpenChange).toHaveBeenCalledWith(false);
  });
});
