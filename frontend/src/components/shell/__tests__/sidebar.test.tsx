// REDESIGN-001 Phase 4.2.a — sidebar IA restructure tests.
//
// Asserts the new operator mental-model groups:
//   Registry / Security / Governance / Integrations / Access
// and verifies that the deleted Platform group is gone, that Audit
// streaming moved into Governance, that Webhooks are under Integrations,
// and that Tenant users is under Access.
import { render, screen, act } from "@testing-library/react";
import { describe, test, expect, vi, beforeEach } from "vitest";
import {
  createRouter,
  createMemoryHistory,
  RouterProvider,
  createRoute,
  createRootRoute,
} from "@tanstack/react-router";
import { Sidebar } from "../sidebar";

// ---------------------------------------------------------------------------
// Module mocks
// Sidebar imports several hooks; we stub them all so the component can
// mount without a real backend or QueryClient.
// ---------------------------------------------------------------------------

// useWorkspace — return a minimal workspace so the brand header renders.
vi.mock("@/lib/api/workspace", () => ({
  useWorkspace: () => ({
    data: { name: "Acme", plan: "pro" },
  }),
}));

// useCacheStats — declared as vi.fn() so individual tests can override the
// return value via mockReturnValue for the probe-gate test. Default returns
// a non-null stats object, making Pull-through cache visible.
const mockUseCacheStats = vi.fn(() => ({ data: { total_manifests: 0 } as unknown }));
vi.mock("@/lib/api/proxy-cache", () => ({
  // Return value is forwarded through mockUseCacheStats so per-test
  // overrides (mockReturnValue) take effect without re-mocking the module.
  useCacheStats: () => mockUseCacheStats(),
}));

// useAuthStore — return null claims. The Platform group no longer exists,
// so the claims value does not affect sidebar rendering in Phase 4.2.a.
vi.mock("@/lib/auth/store", () => ({
  useAuthStore: (selector: (s: { claims: null }) => unknown) =>
    selector({ claims: null }),
}));

// ---------------------------------------------------------------------------
// Render helper
// Sidebar uses TanStack Router <Link> elements, so it must be mounted
// inside a RouterProvider. We build a minimal in-memory router with a
// single root route to satisfy the context requirement.
// ---------------------------------------------------------------------------
async function renderSidebar(initialPath = "/") {
  const rootRoute = createRootRoute({ component: Sidebar });
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

// groupHeadings returns only the sidebar group-header <div> elements,
// identified by their distinctive `tracking-[0.18em]` class. This avoids
// collisions with nav item labels that share the same text content (e.g.
// "Security" appears as both a group heading and a nav link label).
function groupHeadings(): string[] {
  return Array.from(
    document.querySelectorAll(
      ".text-\\[10px\\].uppercase.tracking-\\[0\\.18em\\]",
    ),
  ).map((el) => el.textContent ?? "");
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("Sidebar — REDESIGN-001 Phase 4.2.a operator IA", () => {
  beforeEach(() => {
    mockUseCacheStats.mockReset();
    // Reset to the default non-null response before each test so the
    // probe gate is open (Pull-through cache visible) by default.
    mockUseCacheStats.mockReturnValue({ data: { total_manifests: 0 } });
  });

  // ── Group headings ────────────────────────────────────────────────────────

  test("renders all five group headings in order", async () => {
    await renderSidebar();

    // groupHeadings() targets only the uppercase section header divs so
    // nav item labels that share group names (e.g. "Security") don't
    // cause false matches.
    const headings = groupHeadings();

    expect(headings).toEqual([
      "Registry",
      "Security",
      "Governance",
      "Integrations",
      "Access",
    ]);
  });

  test("does NOT render a Platform group", async () => {
    await renderSidebar();
    // Check only group heading elements, not nav items.
    expect(groupHeadings()).not.toContain("Platform");
  });

  // ── Registry group ────────────────────────────────────────────────────────

  test("Registry group contains Dashboard, Repositories, Pull-through cache", async () => {
    await renderSidebar();
    // Dashboard is the new index-route ("/") entry added at the top of the
    // Registry group.
    expect(screen.getByText("Dashboard")).toBeInTheDocument();
    expect(screen.getByText("Repositories")).toBeInTheDocument();
    // "Helm charts" was retired by the unified-artifact-catalog feature — Helm
    // is now reached via the Repositories view's Charts filter, not a standalone
    // nav item — so it must NOT render.
    expect(screen.queryByText("Helm charts")).not.toBeInTheDocument();
    expect(screen.getByText("Pull-through cache")).toBeInTheDocument();
  });

  // ── Dashboard root-route active match ──────────────────────────────────────

  test("marks Dashboard active ONLY on the exact index route", async () => {
    // On "/", Dashboard is the active section.
    await renderSidebar("/");
    const dashLink = screen.getByText("Dashboard").closest("a");
    expect(dashLink?.getAttribute("aria-current")).toBe("page");
    expect(dashLink?.className).toContain("text-[var(--color-accent)]");
  });

  test("does NOT mark Dashboard active on non-root routes", async () => {
    // Every path starts with "/", so a naive startsWith would light up
    // Dashboard everywhere. The exact-match special-case must keep it dark
    // on child routes like /repositories.
    await renderSidebar("/repositories");
    const dashLink = screen.getByText("Dashboard").closest("a");
    expect(dashLink?.getAttribute("aria-current")).toBeNull();
    expect(dashLink?.className).not.toContain(
      "bg-[var(--color-accent-subtle)]",
    );
  });

  // ── Security group ────────────────────────────────────────────────────────

  test("Security group heading is present", async () => {
    await renderSidebar();
    // Check the group heading specifically, not the nav item.
    expect(groupHeadings()).toContain("Security");
  });

  test("Security nav link is rendered", async () => {
    await renderSidebar();
    // The nav link <span> "Security" is inside an <a> tag.
    // getAllByText returns both the heading div and the nav span — assert
    // that at least one of them is an anchor descendant.
    const securityEls = screen.getAllByText("Security");
    const inAnchor = securityEls.some((el) => el.closest("a") !== null);
    expect(inAnchor).toBe(true);
  });

  // ── Governance group ─────────────────────────────────────────────────────

  test("Governance group contains Activity and Audit streaming", async () => {
    await renderSidebar();
    expect(screen.getByText("Activity")).toBeInTheDocument();
    expect(screen.getByText("Audit streaming")).toBeInTheDocument();
  });

  test("Audit streaming is under Governance (not Integrations)", async () => {
    await renderSidebar();

    // Verify DOM order: Governance heading → Audit streaming → Integrations heading.
    const allNodes = Array.from(document.querySelectorAll("div, span, a")).map(
      (el) => el.textContent?.trim(),
    );

    const govIdx = allNodes.indexOf("Governance");
    const auditIdx = allNodes.indexOf("Audit streaming");
    const intIdx = allNodes.indexOf("Integrations");

    expect(govIdx).toBeGreaterThanOrEqual(0);
    expect(auditIdx).toBeGreaterThan(govIdx);
    expect(intIdx).toBeGreaterThan(auditIdx);
  });

  // ── Integrations group ────────────────────────────────────────────────────

  test("Integrations group contains Webhooks", async () => {
    await renderSidebar();
    expect(screen.getByText("Webhooks")).toBeInTheDocument();
  });

  // ── Access group ─────────────────────────────────────────────────────────

  test("Access group contains Organizations, Tenant users, and API keys", async () => {
    await renderSidebar();
    expect(screen.getByText("Organizations")).toBeInTheDocument();
    expect(screen.getByText("Tenant users")).toBeInTheDocument();
    expect(screen.getByText("API keys")).toBeInTheDocument();
  });

  test("Tenant users is under Access (not its own top-level group)", async () => {
    await renderSidebar();

    // Access must be a group heading (not just a nav item), and it must
    // appear before Tenant users in the DOM.
    const headings = groupHeadings();
    expect(headings).toContain("Access");
    expect(headings).not.toContain("Tenant users");

    const allNodes = Array.from(document.querySelectorAll("div, span")).map(
      (el) => el.textContent?.trim(),
    );
    const accessIdx = allNodes.indexOf("Access");
    const tenantUsersIdx = allNodes.indexOf("Tenant users");
    expect(accessIdx).toBeLessThan(tenantUsersIdx);
  });

  // ── Deleted items ─────────────────────────────────────────────────────────

  test("does NOT render Tenants link (moved to Settings › Platform in 4.2.d)", async () => {
    await renderSidebar();
    expect(screen.queryByText("Tenants")).not.toBeInTheDocument();
  });

  test("does NOT render Scanner link (moved to Settings › Platform in 4.2.d)", async () => {
    await renderSidebar();
    expect(screen.queryByText("Scanner")).not.toBeInTheDocument();
  });

  // ── Probe gate ────────────────────────────────────────────────────────────

  test("hides Pull-through cache when probe returns null (BFF not wired)", async () => {
    // Return data: null for this one test — the sidebar treats
    // proxyCacheStats != null as the gate condition, so null means "feature
    // not wired" and the Pull-through cache link is hidden.
    mockUseCacheStats.mockReturnValue({ data: null });
    await renderSidebar();
    expect(screen.queryByText("Pull-through cache")).not.toBeInTheDocument();
  });

  // ── Settings (bottom-pinned) ──────────────────────────────────────────────

  test("renders bottom-pinned Settings link", async () => {
    await renderSidebar();
    // Settings link is a standalone anchor outside the GROUPS nav, so it
    // should appear exactly once in the document.
    expect(screen.getByText("Settings")).toBeInTheDocument();
  });

  // ── Active-state class ────────────────────────────────────────────────────

  test("marks Repositories link as active when pathname is /repositories", async () => {
    await renderSidebar("/repositories");

    const repoLink = screen.getByText("Repositories").closest("a");
    expect(repoLink?.className).toContain("text-[var(--color-accent)]");
  });

  // ── a11y: aria-current ─────────────────────────────────────────────────────

  test("sets aria-current='page' on the active nav link", async () => {
    await renderSidebar("/repositories");
    const repoLink = screen.getByText("Repositories").closest("a");
    expect(repoLink?.getAttribute("aria-current")).toBe("page");

    // Non-active links must NOT carry aria-current. (Pull-through cache is a
    // stable non-active Registry item; "Helm charts" was retired by the
    // unified-artifact-catalog feature.)
    const cacheLink = screen.getByText("Pull-through cache").closest("a");
    expect(cacheLink?.getAttribute("aria-current")).toBeNull();
  });

  // ── Active-match path boundary ─────────────────────────────────────────────

  test("active match respects path boundaries (child route stays active)", async () => {
    // A deeper child of the route is still the active section.
    await renderSidebar("/repositories/library/nginx");
    const repoLink = screen.getByText("Repositories").closest("a");
    expect(repoLink?.getAttribute("aria-current")).toBe("page");
    expect(repoLink?.className).toContain("text-[var(--color-accent)]");
  });

  // ── Phase 2.4 (RM-006) — plan badge removed ───────────────────────────────

  test("does NOT render workspace.plan badge in the brand block", async () => {
    // The useWorkspace mock returns plan: "pro" — before Phase 2.4 the
    // brand block would render an uppercase "PRO" badge next to the name.
    // Phase 2.4 strips that chrome because self-hosted deployments have
    // no billing surface; the BFF still serves `plan` for the multi-mode
    // admin Tenants page but the personal nav chrome stops surfacing it.
    await renderSidebar();
    // The badge text was uppercased via CSS but the React child node is
    // the raw lowercase string from workspace.plan. Match both forms to
    // catch a regression that re-introduces it in either casing.
    expect(screen.queryByText("pro")).not.toBeInTheDocument();
    expect(screen.queryByText("PRO")).not.toBeInTheDocument();
  });
});
