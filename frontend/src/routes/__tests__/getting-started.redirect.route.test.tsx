import * as React from "react";
import { render, waitFor } from "@testing-library/react";
import { describe, test, expect, vi, beforeEach } from "vitest";
import {
  createRouter,
  createMemoryHistory,
  RouterProvider,
} from "@tanstack/react-router";
import { routeTree } from "@/routeTree.gen";
import type { MeResponse } from "@/lib/api/me";

// ---------------------------------------------------------------------------
// REDESIGN-001 Phase 4.3 §3 — route-guard test for the dashboard's first-run
// onboarding redirect. Section 2's QA review explicitly deferred this test
// to §3; this file is that deferred coverage.
//
// Strategy: render the real routeTree with a mocked useMe(), navigate to "/",
// and assert the router's resulting pathname. The redirect lives in a
// render-time React.useEffect (NOT a beforeLoad), so we mount the index
// component and wait for the effect to fire before asserting.
//
// Four quadrants:
//   1. onboarding_complete === false  → redirects to /getting-started
//   2. onboarding_complete === true   → stays on /
//   3. onboarding_complete undefined  → stays on / (legacy/pre-rollout BFF)
//   4. type === "service_account"     → stays on /  (SAs don't onboard)
// ---------------------------------------------------------------------------

// ── Heavy UI stubs — same pattern as api-keys.service-accounts.route.test.tsx ─

vi.mock("@/components/shell/app-shell", () => ({
  AppShell: ({ children }: { children: React.ReactNode }) =>
    React.createElement("div", { "data-testid": "app-shell" }, children),
}));

vi.mock("@/components/shell/sidebar", () => ({
  Sidebar: () => React.createElement("div", { "data-testid": "sidebar" }),
}));

vi.mock("@/components/shell/topbar", () => ({
  Topbar: () => React.createElement("div", { "data-testid": "topbar" }),
}));

vi.mock("@/components/shell/footer", () => ({
  Footer: () => React.createElement("div", { "data-testid": "footer" }),
}));

vi.mock("@/components/shell/notifications-bell", () => ({
  NotificationsBell: () => null,
}));

vi.mock("@/components/shell/theme-toggle", () => ({
  ThemeToggle: () => null,
}));

// Dashboard subtree — every component is stubbed because none of it should
// render in the redirect test (we expect the gated `aria-busy` div) and
// none of it is what we're asserting against.
vi.mock("@/components/dashboard/stat-card", () => ({
  StatCard: () => null,
}));
vi.mock("@/components/dashboard/storage-card", () => ({
  StorageCard: () => null,
}));
vi.mock("@/components/dashboard/health-card", () => ({
  HealthCard: () => null,
}));
vi.mock("@/components/dashboard/quick-actions", () => ({
  QuickActions: () => null,
}));
vi.mock("@/components/dashboard/analytics-card", () => ({
  AnalyticsCard: () => null,
}));
vi.mock("@/components/dashboard/storage-breakdown-card", () => ({
  StorageBreakdownCard: () => null,
}));
vi.mock("@/components/dashboard/first-steps-strip", () => ({
  FirstStepsStrip: () => null,
}));
vi.mock("@/components/security/severity-bar", () => ({
  SeverityBar: () => null,
}));

// Permissive lucide-react stub. The dashboard + wizard + shell components
// import many icons; listing each explicitly avoids the ESM-export-shape
// problem that a Proxy-based stub causes inside Vitest's tinypool worker.
vi.mock("lucide-react", () => {
  const Stub = () => null;
  // Cover every icon transitively imported by any route the test mounts
  // (dashboard index, getting-started wizard, settings shell, etc.).
  return {
    __esModule: true,
    default: Stub,
    LayoutDashboard: Stub,
    Boxes: Stub,
    ArrowDownToLine: Stub,
    ArrowRight: Stub,
    ShieldAlert: Stub,
    ShieldCheck: Stub,
    Shield: Stub,
    Users: Stub,
    UsersRound: Stub,
    Webhook: Stub,
    Building2: Stub,
    Building: Stub,
    Activity: Stub,
    KeyRound: Stub,
    Globe: Stub,
    ScanLine: Stub,
    LogOut: Stub,
    User: Stub,
    ChevronDown: Stub,
    Bot: Stub,
    Plus: Stub,
    Bell: Stub,
    Check: Stub,
    Copy: Stub,
    Trash2: Stub,
    Pencil: Stub,
    X: Stub,
    Sparkles: Stub,
    Rocket: Stub,
    BookOpen: Stub,
    Terminal: Stub,
    Package: Stub,
    Server: Stub,
    Settings: Stub,
    Ship: Stub,
    Repeat: Stub,
    Radio: Stub,
    Archive: Stub,
  };
});

// ── Auth store + API hook mocks ────────────────────────────────────────────

// JWT token must be truthy so the parent `_authenticated.tsx` guard lets us
// through. Claims are minimal — DashboardHome only reads `claims?.username`.
let mockMe: MeResponse | undefined = undefined;
let mockMeLoading = false;

vi.mock("@/lib/auth/store", () => ({
  useAuthStore: (
    selector: (s: { claims: unknown; token: string | null }) => unknown,
  ) =>
    selector({
      claims: { username: "test-user", sub: "u-1", tenant_id: "t-1", exp: 0, iat: 0, jti: "j" },
      token: "any-token-here",
    }),
  authStore: {
    getToken: () => "any-token-here",
    getClaims: () => ({
      username: "test-user",
      sub: "u-1",
      tenant_id: "t-1",
      exp: 0,
      iat: 0,
      jti: "j",
    }),
    setToken: vi.fn(),
    clear: vi.fn(),
  },
}));

vi.mock("@/lib/api/me", () => ({
  // Variable-driven so each test can set the shape it wants before mount.
  useMe: () => ({ data: mockMe, isLoading: mockMeLoading }),
  // useCompleteOnboarding is referenced by the wizard component, which we
  // stub above, but the import resolution still happens — return a no-op.
  useCompleteOnboarding: () => ({
    mutate: vi.fn(),
    mutateAsync: vi.fn().mockResolvedValue(undefined),
    isPending: false,
  }),
  meKeys: { all: ["me"] as const },
}));

// useStats + useWorkspace are read at render-time but their values are
// irrelevant to the redirect logic — return safe empties.
vi.mock("@/lib/api/stats", () => ({
  useStats: () => ({
    data: undefined,
    isLoading: false,
    isError: false,
    error: undefined,
    refetch: vi.fn(),
  }),
}));
vi.mock("@/lib/api/workspace", () => ({
  useWorkspace: () => ({ data: null, isLoading: false }),
}));

// Sonner + router-devtools stubs to silence loader-side noise.
vi.mock("sonner", () => ({
  Toaster: () => null,
  toast: { success: vi.fn(), error: vi.fn() },
}));
vi.mock("@tanstack/router-devtools", () => ({
  TanStackRouterDevtools: () => null,
}));

// ── Helper ─────────────────────────────────────────────────────────────────

// Builds a memory-history router rooted at the given path and resolves
// the initial load. Mirrors the helper used by the service-accounts test.
async function buildRouter(initialPath: string) {
  const history = createMemoryHistory({ initialEntries: [initialPath] });
  const router = createRouter({ routeTree, history });
  await router.load();
  return router;
}

// ── Tests ──────────────────────────────────────────────────────────────────

describe("/ dashboard onboarding redirect (Phase 4.3 §3)", () => {
  beforeEach(() => {
    mockMe = undefined;
    mockMeLoading = false;
  });

  test("onboarding_complete=false redirects to /getting-started", async () => {
    // Arrange: human user who has not completed onboarding.
    mockMe = {
      type: "user",
      user_id: "u-1",
      username: "alice",
      email: "alice@example.com",
      display_name: "Alice",
      tenant_id: "t-1",
      roles: [],
      onboarding_complete: false,
    } as MeResponse;

    const router = await buildRouter("/");
    render(<RouterProvider router={router} />);

    // The useEffect fires after mount and navigates to /getting-started
    // with replace:true. waitFor handles the async commit + nav.
    await waitFor(() => {
      expect(router.state.location.pathname).toBe("/getting-started");
    });
  });

  test("onboarding_complete=true stays on /", async () => {
    mockMe = {
      type: "user",
      user_id: "u-1",
      username: "alice",
      email: "alice@example.com",
      display_name: "Alice",
      tenant_id: "t-1",
      roles: [],
      onboarding_complete: true,
    } as MeResponse;

    const router = await buildRouter("/");
    render(<RouterProvider router={router} />);

    // Give the redirect effect time to NOT fire. waitFor with a short
    // poll window asserts the negative case: if the redirect were going
    // to fire, it would have done so by now.
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(router.state.location.pathname).toBe("/");
  });

  test("onboarding_complete=undefined stays on / (pre-rollout BFF compat)", async () => {
    // Legacy backend that doesn't emit the column at all.
    mockMe = {
      type: "user",
      user_id: "u-1",
      username: "alice",
      email: "alice@example.com",
      display_name: "Alice",
      tenant_id: "t-1",
      roles: [],
      // onboarding_complete intentionally omitted
    } as MeResponse;

    const router = await buildRouter("/");
    render(<RouterProvider router={router} />);

    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(router.state.location.pathname).toBe("/");
  });

  test("type=service_account stays on / even when flag is false", async () => {
    // SA with onboarding_complete=false should still not redirect — SAs
    // are CI bots that should land on the dashboard, and the BE rejects
    // POST /users/me/onboarding/complete from SA principals with 403.
    mockMe = {
      type: "service_account",
      id: "shadow-uuid",
      tenant_id: "t-1",
      email: null,
      display_name: "ci-bot",
      roles: [],
      onboarding_complete: false,
      service_account: {
        id: "sa-1",
        name: "ci-bot",
        description: "",
        allowed_scopes: [],
      },
    } as MeResponse;

    const router = await buildRouter("/");
    render(<RouterProvider router={router} />);

    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(router.state.location.pathname).toBe("/");
  });
});
