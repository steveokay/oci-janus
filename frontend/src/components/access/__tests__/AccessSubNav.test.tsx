import { render, screen, act, fireEvent } from "@testing-library/react";
import { describe, test, expect, vi, beforeEach } from "vitest";
import {
  createRouter,
  createMemoryHistory,
  RouterProvider,
  createRoute,
  createRootRoute,
} from "@tanstack/react-router";
import { AccessSubNav } from "../AccessSubNav";
import type { JanusJwtClaims } from "@/lib/auth/jwt";

// ---------------------------------------------------------------------------
// Mock the auth store so we can control what `claims` the component sees
// without needing a real JWT. `useAuthStore` is a Zustand selector hook;
// we replace the entire module with a factory that returns whatever `mockClaims`
// is set to at the time the hook is called.
// ---------------------------------------------------------------------------
let mockClaims: JanusJwtClaims | null = null;

vi.mock("@/lib/auth/store", () => ({
  // `useAuthStore` is called with a selector: `(s) => s.claims`
  // We satisfy the selector pattern by returning mockClaims directly.
  useAuthStore: (selector: (s: { claims: JanusJwtClaims | null }) => unknown) =>
    selector({ claims: mockClaims }),
  authStore: {
    getToken: () => null,
    getClaims: () => mockClaims,
    setToken: vi.fn(),
    clear: vi.fn(),
  },
}));

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

// Workspace reader — roles array contains only "reader". Should not see any
// workspace-admin surfaces.
const readerClaims: JanusJwtClaims = {
  sub: "user-reader",
  tenant_id: "tenant-1",
  username: "regular-user",
  exp: Math.floor(Date.now() / 1000) + 3600,
  iat: Math.floor(Date.now() / 1000),
  jti: "jti-reader",
  roles: ["reader"],
};

// Workspace writer — same outcome as reader for nav gating.
const writerClaims: JanusJwtClaims = {
  sub: "user-writer",
  tenant_id: "tenant-1",
  username: "writer-user",
  exp: Math.floor(Date.now() / 1000) + 3600,
  iat: Math.floor(Date.now() / 1000),
  jti: "jti-writer",
  roles: ["writer"],
};

// Workspace admin — `admin` role on an org scope (no platform `*` marker).
// `isWorkspaceAdmin` returns true; should see workspace surfaces.
const workspaceAdminClaims: JanusJwtClaims = {
  sub: "user-workspace-admin",
  tenant_id: "tenant-1",
  username: "workspace-admin-user",
  exp: Math.floor(Date.now() / 1000) + 3600,
  iat: Math.floor(Date.now() / 1000),
  jti: "jti-workspace-admin",
  roles: ["admin"],
};

// Workspace owner — `owner` is a strict superset of admin. Should also see
// workspace surfaces even when "admin" is not in the dedupe.
const workspaceOwnerClaims: JanusJwtClaims = {
  sub: "user-owner",
  tenant_id: "tenant-1",
  username: "owner-user",
  exp: Math.floor(Date.now() / 1000) + 3600,
  iat: Math.floor(Date.now() / 1000),
  jti: "jti-owner",
  roles: ["owner"],
};

// Platform admin — flat-roles JWT can't distinguish from workspace admin
// (the scope info isn't in the JWT), but the surface contract must continue
// to render workspace links for them. Kept as a regression fixture.
const platformAdminClaims: JanusJwtClaims = {
  sub: "user-platform-admin",
  tenant_id: "tenant-1",
  username: "platform-admin-user",
  exp: Math.floor(Date.now() / 1000) + 3600,
  iat: Math.floor(Date.now() / 1000),
  jti: "jti-platform-admin",
  roles: ["admin"],
};

// Back-compat aliases — older tests below reference these names.
const nonAdminClaims = readerClaims;
const adminClaims = workspaceAdminClaims;

// ---------------------------------------------------------------------------
// Router wrapper — AccessSubNav renders TanStack Router <Link> elements, so
// it must be mounted inside a RouterProvider. We build a minimal in-memory
// router with just a root route to satisfy the context requirement without
// needing any real route tree.
// ---------------------------------------------------------------------------
async function renderSubNav() {
  // Build a minimal route tree: root renders AccessSubNav directly.
  const rootRoute = createRootRoute({
    component: AccessSubNav,
  });

  // Add a stub index child route so the router has a matched route to render.
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

  // `router.load()` resolves the initial location, then wrapping the render in
  // `act` flushes all React state updates from the router's navigation effect.
  await router.load();

  await act(async () => {
    render(<RouterProvider router={router} />);
  });
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("AccessSubNav", () => {
  beforeEach(() => {
    // Reset claims before each test so state from a previous test doesn't leak.
    mockClaims = null;
    // DSGN-011 — the Preview section now collapses behind a flyout that
    // persists in localStorage. Reset between tests so a previous test's
    // "open" state doesn't leak; tests that need it open call
    // openPreviewSection() explicitly.
    try {
      window.localStorage.removeItem("accessSubNav.previewOpen");
    } catch {
      // jsdom always has localStorage; the try/catch matches the
      // component's defensive read so the test mirrors prod behaviour.
    }
  });

  // openPreviewSection — finds the collapsible "Preview" expander button by
  // its aria-controls binding and clicks it. Used by tests that need the
  // four FUT-001..FUT-004 preview links visible.
  function openPreviewSection(): void {
    const button = document.querySelector(
      '[aria-controls="access-subnav-preview-items"]',
    );
    if (button instanceof HTMLElement) {
      fireEvent.click(button);
    }
  }

  test("hides Workspace and Preview sections for non-admin users", async () => {
    mockClaims = nonAdminClaims;
    await renderSubNav();

    // The "Yours" section is always visible — verify we rendered something.
    expect(screen.getByText("Yours")).toBeInTheDocument();

    // Admin-only sections must be absent from the DOM entirely.
    expect(screen.queryByText("Workspace")).not.toBeInTheDocument();
    expect(screen.queryByText("Preview")).not.toBeInTheDocument();
  });

  test("shows Yours, Workspace, and Preview sections for admin users", async () => {
    mockClaims = adminClaims;
    await renderSubNav();

    // All three section headings must be present for platform-admins.
    expect(screen.getByText("Yours")).toBeInTheDocument();
    expect(screen.getByText("Workspace")).toBeInTheDocument();

    // DSGN-011 — the Preview section heading is now a collapsible <button>
    // with `aria-controls="access-subnav-preview-items"`. Presence of the
    // expander is what "section exists" means now; the items themselves
    // only mount when expanded.
    const expander = document.querySelector(
      '[aria-controls="access-subnav-preview-items"]',
    );
    expect(expander).toBeInTheDocument();
    expect(expander).toHaveAttribute("aria-expanded", "false");
  });

  test("shows Personal keys link for all users", async () => {
    mockClaims = nonAdminClaims;
    await renderSubNav();

    expect(screen.getByText("Personal keys")).toBeInTheDocument();
  });

  test("Service accounts and Activity links are absent for non-admins", async () => {
    // Non-admin: admin-only items must not appear.
    mockClaims = nonAdminClaims;
    await renderSubNav();
    expect(screen.queryByText("Service accounts")).not.toBeInTheDocument();
    expect(screen.queryByText("Activity")).not.toBeInTheDocument();
  });

  test("shows remaining preview links for admins after expanding the flyout", async () => {
    mockClaims = adminClaims;
    await renderSubNav();

    // DSGN-011 — the Preview section is collapsed by default. Verify the
    // link is hidden, then expand and verify it appears.
    // FUT-001 shipped 2026-07-01 — "Federated trust" graduated to the
    // Workspace section and is asserted in its own test below.
    // FUT-002 shipped 2026-06-30 — "Credential helpers" graduated to the
    // Workspace section and is asserted in its own test below.
    // FUT-003 shipped 2026-07-01 — "Token policies" graduated to the
    // Workspace section and is asserted in its own test below.
    expect(screen.queryByText("Access review")).not.toBeInTheDocument();
    openPreviewSection();

    expect(screen.getByText("Access review")).toBeInTheDocument();
  });

  // FUT-002 graduation regression: Credential helpers must render in the
  // always-visible Workspace section for admins, NOT inside the collapsed
  // Preview flyout.
  test("shows Credential helpers in Workspace section for admins without expansion", async () => {
    mockClaims = adminClaims;
    await renderSubNav();

    expect(screen.getByText("Credential helpers")).toBeInTheDocument();
    // Confirm the Preview flyout is still collapsed — the link is in the
    // Workspace section above it, not behind the expander.
    const expander = document.querySelector(
      '[aria-controls="access-subnav-preview-items"]',
    );
    expect(expander).toHaveAttribute("aria-expanded", "false");
  });

  // FUT-001 graduation regression: Federated trust must render in the
  // always-visible Workspace section for admins, NOT inside the collapsed
  // Preview flyout. Mirrors the FUT-002 assertion above.
  test("shows Federated trust in Workspace section for admins without expansion", async () => {
    mockClaims = adminClaims;
    await renderSubNav();

    expect(screen.getByText("Federated trust")).toBeInTheDocument();
    const expander = document.querySelector(
      '[aria-controls="access-subnav-preview-items"]',
    );
    expect(expander).toHaveAttribute("aria-expanded", "false");
  });

  // FUT-003 graduation regression: Token policies must render in the
  // always-visible Workspace section for admins, NOT inside the collapsed
  // Preview flyout. Mirrors the FUT-001/FUT-002 assertions above.
  test("shows Token policies in Workspace section for admins without expansion", async () => {
    mockClaims = adminClaims;
    await renderSubNav();

    expect(screen.getByText("Token policies")).toBeInTheDocument();
    const expander = document.querySelector(
      '[aria-controls="access-subnav-preview-items"]',
    );
    expect(expander).toHaveAttribute("aria-expanded", "false");
  });

  // DSGN-011 — collapsing the Preview section persists in localStorage so
  // an admin who explicitly closes it doesn't have to re-close on every
  // navigation back into /api-keys.
  test("preview flyout state persists in localStorage", async () => {
    mockClaims = adminClaims;
    await renderSubNav();

    // Start collapsed (beforeEach cleared the key).
    let expander = document.querySelector(
      '[aria-controls="access-subnav-preview-items"]',
    );
    expect(expander).toHaveAttribute("aria-expanded", "false");

    // Open it, and verify localStorage is updated.
    openPreviewSection();
    expander = document.querySelector(
      '[aria-controls="access-subnav-preview-items"]',
    );
    expect(expander).toHaveAttribute("aria-expanded", "true");
    expect(window.localStorage.getItem("accessSubNav.previewOpen")).toBe(
      "true",
    );
  });

  // DSGN-001 — verify the workspace-admin gate (not platform-admin) drives
  // visibility. A tenant admin without the platform marker must see Service
  // accounts + Activity; workspace writers / readers must not.

  test("workspace admin sees Service accounts and Activity", async () => {
    mockClaims = workspaceAdminClaims;
    await renderSubNav();

    expect(screen.getByText("Service accounts")).toBeInTheDocument();
    expect(screen.getByText("Activity")).toBeInTheDocument();
  });

  test("workspace owner sees Service accounts and Activity", async () => {
    mockClaims = workspaceOwnerClaims;
    await renderSubNav();

    expect(screen.getByText("Service accounts")).toBeInTheDocument();
    expect(screen.getByText("Activity")).toBeInTheDocument();
  });

  test("workspace writer does not see Service accounts or Activity", async () => {
    mockClaims = writerClaims;
    await renderSubNav();

    expect(screen.queryByText("Service accounts")).not.toBeInTheDocument();
    expect(screen.queryByText("Activity")).not.toBeInTheDocument();
  });

  test("workspace reader does not see Service accounts or Activity", async () => {
    mockClaims = readerClaims;
    await renderSubNav();

    expect(screen.queryByText("Service accounts")).not.toBeInTheDocument();
    expect(screen.queryByText("Activity")).not.toBeInTheDocument();
  });

  test("platform admin still sees Service accounts and Activity", async () => {
    mockClaims = platformAdminClaims;
    await renderSubNav();

    expect(screen.getByText("Service accounts")).toBeInTheDocument();
    expect(screen.getByText("Activity")).toBeInTheDocument();
  });
});
