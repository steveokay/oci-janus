import * as React from "react";
import { render, screen, waitFor } from "@testing-library/react";
import { describe, test, expect, vi, beforeEach } from "vitest";
import {
  createRouter,
  createMemoryHistory,
  RouterProvider,
} from "@tanstack/react-router";
import { routeTree } from "@/routeTree.gen";
import type { JanusJwtClaims } from "@/lib/auth/jwt";

// ---------------------------------------------------------------------------
// Strategy: test the `beforeLoad` redirect logic via the actual routeTree so
// we exercise the guard as it runs in production. Heavy leaf-level components
// (AppShell, Sidebar, Topbar, ServiceAccountsTable, etc.) are replaced with
// lightweight stubs so the test stays fast and doesn't need API wiring.
//
// The parent `_authenticated` route checks `authStore.getToken()`.
// The service-accounts route checks `authStore.getClaims()` via `isPlatformAdmin`.
// Both are imperatively read from the Zustand store, so we control them by
// directly mutating the store via `authStore.setToken` / setting state.
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Stub heavy UI components that would need complex provider trees or make
// network calls. Route guards run in `beforeLoad`, before these components
// mount, but we still need safe stub renderers for routes that DO render.
// ---------------------------------------------------------------------------

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

vi.mock("@/components/access/AccessSubNav", () => ({
  AccessSubNav: () =>
    React.createElement("nav", { "data-testid": "access-sub-nav" }),
}));

vi.mock("@/components/access/ServiceAccountsTable", () => ({
  ServiceAccountsTable: () =>
    React.createElement("div", { "data-testid": "service-accounts-table" }),
}));

vi.mock("@/components/access/CreateServiceAccountDialog", () => ({
  CreateServiceAccountDialog: () =>
    React.createElement("div", { "data-testid": "create-sa-dialog" }),
}));

// Stub all remaining dashboard-level components that mount on authenticated routes.
vi.mock("@/components/shell/notifications-bell", () => ({
  NotificationsBell: () => null,
}));

vi.mock("@/components/shell/theme-toggle", () => ({
  ThemeToggle: () => null,
}));

// Stub the API keys index page — the redirect target for non-admins.
vi.mock("@/components/access/AccessHubLayout", () => ({
  AccessHubLayout: ({ children }: { children: React.ReactNode }) =>
    React.createElement("div", { "data-testid": "access-hub-layout" }, children),
}));

// Stub lucide-react icon components used by sidebar / topbar.
vi.mock("lucide-react", () => ({
  // Provide a permissive stub for any icon the component tree requests.
  __esModule: true,
  default: () => null,
  // Named exports for specific icons referenced in the sidebar/topbar stubs.
  LayoutDashboard: () => null,
  Boxes: () => null,
  ShieldCheck: () => null,
  Users: () => null,
  Webhook: () => null,
  Building2: () => null,
  Activity: () => null,
  KeyRound: () => null,
  Globe: () => null,
  ScanLine: () => null,
  LogOut: () => null,
  User: () => null,
  ChevronDown: () => null,
  Bot: () => null,
  Plus: () => null,
  ShieldAlert: () => null,
}));

// ---------------------------------------------------------------------------
// Auth store — control token and claims from tests.
// ---------------------------------------------------------------------------
let mockToken: string | null = null;
let mockClaims: JanusJwtClaims | null = null;

vi.mock("@/lib/auth/store", () => ({
  useAuthStore: (selector: (s: { claims: JanusJwtClaims | null; token: string | null }) => unknown) =>
    selector({ claims: mockClaims, token: mockToken }),
  authStore: {
    getToken: () => mockToken,
    getClaims: () => mockClaims,
    setToken: vi.fn(),
    clear: vi.fn(),
  },
}));

// Stub tanstack-query hooks used deep in the component tree to avoid needing
// a QueryClientProvider. We return safe empty states.
vi.mock("@/lib/api/workspace", () => ({
  useWorkspace: () => ({ data: null, isLoading: false }),
}));

vi.mock("@/lib/api/me", () => ({
  useMe: () => ({ data: null }),
}));

vi.mock("@/lib/api/notifications", () => ({
  useNotifications: () => ({ data: null }),
}));

vi.mock("@/lib/api/api-keys", () => ({
  useApiKeys: () => ({ data: null, isLoading: false }),
}));

vi.mock("@/lib/api/service-accounts", () => ({
  useServiceAccounts: () => ({ data: null, isLoading: false }),
  useCreateServiceAccount: () => ({ mutate: vi.fn(), isPending: false }),
}));

// Stub sonner toaster — it registers timers that vitest can complain about.
vi.mock("sonner", () => ({
  Toaster: () => null,
  toast: { success: vi.fn(), error: vi.fn() },
}));

// Stub the router devtools lazy import that __root.tsx tries to do in DEV.
vi.mock("@tanstack/router-devtools", () => ({
  TanStackRouterDevtools: () => null,
}));

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

// Non-admin token stub — value doesn't matter since authStore is mocked.
const NON_ADMIN_TOKEN = "non-admin-stub-token";
const nonAdminClaims: JanusJwtClaims = {
  sub: "user-reader",
  tenant_id: "tenant-1",
  username: "reader",
  exp: Math.floor(Date.now() / 1000) + 3600,
  iat: Math.floor(Date.now() / 1000),
  jti: "jti-reader",
  roles: ["reader"],
};

const ADMIN_TOKEN = "admin-stub-token";
const adminClaims: JanusJwtClaims = {
  sub: "user-admin",
  tenant_id: "tenant-1",
  username: "admin",
  exp: Math.floor(Date.now() / 1000) + 3600,
  iat: Math.floor(Date.now() / 1000),
  jti: "jti-admin",
  roles: ["admin"],
};

// ---------------------------------------------------------------------------
// Helper: build a memory router from the real routeTree, navigate to an
// initial URL, and wait for the router to settle before returning.
// ---------------------------------------------------------------------------
async function buildRouter(initialPath: string) {
  const history = createMemoryHistory({ initialEntries: [initialPath] });
  const router = createRouter({ routeTree, history });
  await router.load();
  return router;
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("/api-keys/service-accounts route guard", () => {
  beforeEach(() => {
    // Reset auth state before each test.
    mockToken = null;
    mockClaims = null;
  });

  test("non-admin loading /api-keys/service-accounts redirects to /api-keys", async () => {
    // Arrange: authenticated as a non-admin user.
    mockToken = NON_ADMIN_TOKEN;
    mockClaims = nonAdminClaims;

    const router = await buildRouter("/api-keys/service-accounts");

    // The beforeLoad guard in service-accounts.tsx throws redirect({ to: "/api-keys" })
    // for non-admins. The router should have landed on /api-keys after resolving.
    expect(router.state.location.pathname).toBe("/api-keys");
  });

  test("admin loading /api-keys/service-accounts renders the page", async () => {
    // Arrange: authenticated as a platform-admin.
    mockToken = ADMIN_TOKEN;
    mockClaims = adminClaims;

    const router = await buildRouter("/api-keys/service-accounts");

    render(<RouterProvider router={router} />);

    // The router must remain at /api-keys/service-accounts — no redirect fired.
    expect(router.state.location.pathname).toBe("/api-keys/service-accounts");

    // The stub service-accounts table confirms the page component mounted.
    await waitFor(() => {
      expect(screen.getByTestId("service-accounts-table")).toBeInTheDocument();
    });
  });
});
