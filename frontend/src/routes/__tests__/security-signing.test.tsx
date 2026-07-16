import { describe, it, expect, vi } from "vitest";
import { render, screen, act } from "@testing-library/react";
import {
  createRouter,
  createMemoryHistory,
  RouterProvider,
  createRoute,
  createRootRoute,
} from "@tanstack/react-router";

// Mock the data hook so the route renders deterministically.
vi.mock("@/lib/api/signing-coverage", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api/signing-coverage")>(
    "@/lib/api/signing-coverage",
  );
  return { ...actual, useSigningCoverage: vi.fn() };
});

import { useSigningCoverage } from "@/lib/api/signing-coverage";
import { SigningTab } from "../_authenticated.security.signing";

const mockHook = vi.mocked(useSigningCoverage);

// SigningTab renders SigningCoverageTable, which renders TanStack Router <Link>
// elements (the per-repo drill-in), so the component must be mounted inside a
// RouterProvider. We build a minimal in-memory router whose root renders the
// element under test — the same lightweight pattern used by the Task 4
// coverage-table test, AccessSubNav.test.tsx, and sidebar.test.tsx. The stub
// only needs to supply router *context* at runtime.
async function renderWithRouter(ui: React.ReactElement): Promise<void> {
  const rootRoute = createRootRoute({ component: () => ui });
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

describe("SigningTab", () => {
  it("shows the 'signing not wired' state when signer is disabled", async () => {
    mockHook.mockReturnValue({
      data: { window: 50, signer_enabled: false, summary: {} as never, repos: [] },
      isLoading: false,
      isError: false,
    } as never);
    await renderWithRouter(<SigningTab />);
    expect(screen.getByText(/signing is not wired/i)).toBeInTheDocument();
  });

  it("renders the summary + table when coverage is present", async () => {
    mockHook.mockReturnValue({
      data: {
        window: 50,
        signer_enabled: true,
        summary: {
          repo_count: 1,
          repos_require_signature: 1,
          repos_enforced_empty_allowlist: 0,
          workspace_signed_tag_pct: 0.95,
        },
        repos: [
          {
            org: "acme",
            repo: "api",
            require_signature: true,
            window: 50,
            tags_in_window: 40,
            signed_tags: 38,
            signed_pct: 0.95,
            trusted_key_count: 2,
            allowlist_health: "enforced_with_allowlist",
            stale_trusted_keys: 0,
            recent_signers: [],
          },
        ],
      },
      isLoading: false,
      isError: false,
    } as never);
    await renderWithRouter(<SigningTab />);
    expect(screen.getByText("acme/api")).toBeInTheDocument();
    expect(screen.getByText(/most-recent tags/i)).toBeInTheDocument();
  });
});
