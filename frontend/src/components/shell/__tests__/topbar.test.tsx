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
import { describe, test, expect, vi, beforeEach } from "vitest";
import {
  createRouter,
  createMemoryHistory,
  RouterProvider,
  createRoute,
  createRootRoute,
} from "@tanstack/react-router";
import { Topbar } from "../topbar";

// ── Module mocks — Topbar reaches useAuthStore + useMe + logout fn ───────

// Topbar's tenant UUID chip lives inside the avatar dropdown. The dropdown
// is rendered conditionally on `me.type !== "service_account"` AND the
// chip is gated by deployment mode (Phase 2.5 / RM-007). We override
// useDeploymentInfo per test via the shared mockDeploymentInfo() factory
// so each case can pick its mode.
type DeploymentInfoResult = {
  data: { deployment_mode: "single" | "multi"; version: string } | undefined;
};
const mockDeploymentInfo = vi.fn(
  (): DeploymentInfoResult => ({
    data: { deployment_mode: "multi", version: "test" },
  }),
);

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

vi.mock("@/lib/api/deployment-info", () => ({
  useDeploymentInfo: () => mockDeploymentInfo(),
  // The component now also imports `isSingleMode` from this module. We
  // re-implement the predicate against the mock state so the test sees
  // the same behaviour as the real helper without having to remember to
  // also mock it.
  isSingleMode: (info: { deployment_mode: "single" | "multi" } | undefined) =>
    info?.deployment_mode === "single",
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

// Phase 2.5 / RM-007 — tenant UUID chip is gated on deployment mode.
describe("Topbar — tenant UUID chip mode gate", () => {
  // Each test resets the deployment-info mock and opens the avatar
  // dropdown so the chip's container (and therefore the chip itself, if
  // rendered) is in the DOM.
  beforeEach(() => {
    mockDeploymentInfo.mockReset();
  });

  test("multi mode shows the tenant UUID in the avatar dropdown", async () => {
    mockDeploymentInfo.mockReturnValue({
      data: { deployment_mode: "multi", version: "test" },
    });
    await renderTopbar();
    const user = userEvent.setup();
    // The dropdown trigger is the avatar button (the one without the
    // hamburger label) — easiest selector is "alice" (the mocked username).
    await user.click(screen.getByRole("button", { name: /alice/i }));
    expect(screen.getByText("t-uuid-123")).toBeInTheDocument();
  });

  test("single mode hides the tenant UUID in the avatar dropdown", async () => {
    mockDeploymentInfo.mockReturnValue({
      data: { deployment_mode: "single", version: "test" },
    });
    await renderTopbar();
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: /alice/i }));
    expect(screen.queryByText("t-uuid-123")).not.toBeInTheDocument();
  });

  test("cold cache (data undefined) defaults to chip-visible", async () => {
    // First render before useDeploymentInfo has resolved. isSingleMode()
    // returns false for undefined input → chip renders. This pins the
    // documented invariant: defer to multi-mode behaviour during cold
    // load so single-mode-specific UI never flashes for a multi-mode
    // operator. A future refactor that flipped the default to single
    // would silently change operator-visible behaviour; this test
    // catches it.
    mockDeploymentInfo.mockReturnValue({ data: undefined });
    await renderTopbar();
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: /alice/i }));
    expect(screen.getByText("t-uuid-123")).toBeInTheDocument();
  });
});
