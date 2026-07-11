import * as React from "react";
import { render, waitFor, screen } from "@testing-library/react";
import { describe, test, expect, vi, beforeEach } from "vitest";
import { createRouter, createMemoryHistory, RouterProvider } from "@tanstack/react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { routeTree } from "@/routeTree.gen";
import type { OrgsListResponse } from "@/lib/api/orgs";

// Route-guard style test for the environments overview at /repositories.
// Mirrors the memory-history harness in getting-started.redirect.route.test.tsx:
// render the real routeTree with mocked data hooks, then assert on rendered
// content + the router's resulting pathname (the single-org redirect lives in
// a render-time React.useEffect, so waitFor lets the effect commit + navigate).

// Standard shell/icon stubs (same rationale as the getting-started route test).
vi.mock("@/components/shell/app-shell", () => ({ AppShell: ({ children }: { children: React.ReactNode }) => React.createElement("div", null, children) }));
vi.mock("@/components/shell/sidebar", () => ({ Sidebar: () => null }));
vi.mock("@/components/shell/topbar", () => ({ Topbar: () => null }));
vi.mock("@/components/shell/footer", () => ({ Footer: () => null }));
vi.mock("@/components/shell/notifications-bell", () => ({ NotificationsBell: () => null }));
vi.mock("@/components/shell/theme-toggle", () => ({ ThemeToggle: () => null }));
vi.mock("@tanstack/router-devtools", () => ({ TanStackRouterDevtools: () => null }));
vi.mock("sonner", () => ({ Toaster: () => null, toast: { success: vi.fn(), error: vi.fn() } }));

vi.mock("@/lib/auth/store", () => ({
  useAuthStore: (sel: (s: { claims: unknown; token: string | null }) => unknown) =>
    sel({ claims: { username: "u", sub: "u-1", tenant_id: "t-1", exp: 0, iat: 0, jti: "j" }, token: "tok" }),
  authStore: { getToken: () => "tok", getClaims: () => ({ sub: "u-1" }), setToken: vi.fn(), clear: vi.fn() },
}));

let mockOrgs: OrgsListResponse | undefined;
let mockLoading = false;
vi.mock("@/lib/api/orgs", () => ({
  useOrgs: () => ({ data: mockOrgs, isLoading: mockLoading, isError: false, error: undefined, refetch: vi.fn() }),
  orgKeys: { all: ["orgs"] as const, list: () => ["orgs", "list"] as const },
}));
// The per-org route imports useRepositories; stub it so mounting the tree is cheap.
vi.mock("@/lib/api/repositories", () => ({
  useRepositories: () => ({ data: undefined, isLoading: false, isError: false, error: undefined, refetch: vi.fn(), fetchNextPage: vi.fn(), hasNextPage: false, isFetchingNextPage: false }),
  useCreateRepository: () => ({ mutateAsync: vi.fn() }),
}));

async function buildRouter(path: string) {
  const history = createMemoryHistory({ initialEntries: [path] });
  const router = createRouter({ routeTree, history });
  await router.load();
  return router;
}
function renderRouter(router: Awaited<ReturnType<typeof buildRouter>>) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={qc}><RouterProvider router={router} /></QueryClientProvider>);
}

describe("/repositories environments overview", () => {
  beforeEach(() => { mockOrgs = undefined; mockLoading = false; });

  test("renders a card per org", async () => {
    mockOrgs = { orgs: [
      { org_id: "o1", org: "dev", repo_count: 3, storage_used_bytes: 2048, last_activity_at: "2026-07-10T00:00:00Z" },
      { org_id: "o2", org: "prod", repo_count: 1, storage_used_bytes: 0 },
    ] };
    const router = await buildRouter("/repositories");
    renderRouter(router);
    await waitFor(() => {
      expect(screen.getByText("dev")).toBeInTheDocument();
      expect(screen.getByText("prod")).toBeInTheDocument();
    });
  });

  test("single org redirects straight to that environment", async () => {
    mockOrgs = { orgs: [{ org_id: "o1", org: "dev", repo_count: 3, storage_used_bytes: 2048 }] };
    const router = await buildRouter("/repositories");
    renderRouter(router);
    await waitFor(() => {
      expect(router.state.location.pathname).toBe("/repositories/dev");
    });
  });

  test("zero orgs shows the empty state", async () => {
    mockOrgs = { orgs: [] };
    const router = await buildRouter("/repositories");
    renderRouter(router);
    await waitFor(() => {
      expect(screen.getByText(/No repositories yet/i)).toBeInTheDocument();
    });
  });
});
