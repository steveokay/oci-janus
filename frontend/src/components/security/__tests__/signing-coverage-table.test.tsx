import { describe, it, expect } from "vitest";
import { render, screen, act } from "@testing-library/react";
import {
  createRouter,
  createMemoryHistory,
  RouterProvider,
  createRoute,
  createRootRoute,
} from "@tanstack/react-router";
import type { RepoCoverage } from "@/lib/api/signing-coverage";
import { SigningCoverageTable } from "../signing-coverage-table";

const repos: RepoCoverage[] = [
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
    recent_signers: [
      { key_id: "key-1", last_signed_at: "2026-07-16T00:00:00Z", tag_count: 12 },
    ],
  },
  {
    org: "acme",
    repo: "web",
    require_signature: true,
    window: 50,
    tags_in_window: 10,
    signed_tags: 3,
    signed_pct: 0.3,
    trusted_key_count: 0,
    allowlist_health: "enforced_any_signature",
    stale_trusted_keys: 0,
    recent_signers: [],
  },
];

// SigningCoverageTable renders TanStack Router <Link> elements (the per-repo
// drill-in), so the component must be mounted inside a RouterProvider. We build
// a minimal in-memory router whose root renders the element under test — the
// same lightweight pattern used by AccessSubNav.test.tsx and sidebar.test.tsx.
// The <Link to=...> typechecks against the app's registered route tree, not this
// stub router, so the stub only needs to supply router *context* at runtime.
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

describe("SigningCoverageTable", () => {
  it("renders a row per repo with coverage and health", async () => {
    await renderWithRouter(<SigningCoverageTable repos={repos} />);
    expect(screen.getByText("acme/api")).toBeInTheDocument();
    expect(screen.getByText("acme/web")).toBeInTheDocument();
    expect(screen.getByText("38/40")).toBeInTheDocument();
    expect(screen.getByText(/any signature/i)).toBeInTheDocument();
  });
});
